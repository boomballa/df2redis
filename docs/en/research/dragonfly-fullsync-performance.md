# Dragonfly Full Sync High-Performance Write Mechanism Analysis

## Research Context

When implementing df2redis, I needed to understand why Dragonfly's full sync is extremely fast and how to apply these optimizations to writing to Redis/Redis Cluster. By analyzing Dragonfly source code (`src/server/replica.cc` and `src/server/rdb_load.cc`), I documented the key architectural designs and optimization techniques below.

## **Core Optimization Techniques**

Dragonfly's full sync is extremely fast thanks to these key architectural designs and optimization techniques:

### **1. Multi-flow Parallel Replication Architecture**

Dragonfly uses multiple independent connection streams to transmit data in parallel, with each shard corresponding to an independent replication flow:

`replica.cc:471-597`

```cpp
// Initialize and start sub-replica for each flow.
error_code Replica::InitiateDflySync() {
  auto start_time = absl::Now();

  // Initialize MultiShardExecution.
  multi_shard_exe_.reset(new MultiShardExecution());

  // Initialize shard flows.
  shard_flows_.resize(master_context_.num_flows);
  DCHECK(!shard_flows_.empty());
  for (unsigned i = 0; i < shard_flows_.size(); ++i) {
    shard_flows_[i].reset(
        new DflyShardReplica(server(), master_context_, i, &service_, multi_shard_exe_));
  }
  thread_flow_map_ = Partition(shard_flows_.size());

  // Blocked on until all flows got full sync cut.
  BlockingCounter sync_block{unsigned(shard_flows_.size())};

  // Switch to new error handler that closes flow sockets.
  auto err_handler = [this, sync_block](const auto& ge) mutable {
    // Unblock this function.
    sync_block->Cancel();

    // Make sure the flows are not in a state transition
    lock_guard lk{flows_op_mu_};

    // Unblock all sockets.
    DefaultErrorHandler(ge);
    for (auto& flow : shard_flows_)
      flow->Cancel();
  };

  RETURN_ON_ERR(exec_st_.SwitchErrorHandler(std::move(err_handler)));

  // Make sure we're in LOADING state.
  if (!service_.RequestLoadingState()) {
    return exec_st_.ReportError(std::make_error_code(errc::state_not_recoverable),
                                "Failed to enter LOADING state");
  }

  // Start full sync flows.
  state_mask_.fetch_or(R_SYNCING);

  absl::Cleanup cleanup = [this]() {
    // We do the following operations regardless of outcome.
    JoinDflyFlows();
    service_.RemoveLoadingState();
    state_mask_.fetch_and(~R_SYNCING);
    last_journal_LSNs_.reset();
  };

  std::string_view sync_type = "full";
  {
    unsigned num_df_flows = shard_flows_.size();
    // Going out of the way to avoid using std::vector<bool>...
    auto is_full_sync = std::make_unique<bool[]>(num_df_flows);
    DCHECK(!last_journal_LSNs_ || last_journal_LSNs_->size() == num_df_flows);
    auto shard_cb = [&](unsigned index, auto*) {
      for (auto id : thread_flow_map_[index]) {
        auto ec = shard_flows_[id]->StartSyncFlow(sync_block, &exec_st_,
                                                  last_journal_LSNs_.has_value()
                                                      ? std::optional((*last_journal_LSNs_)[id])
                                                      : std::nullopt);
        if (ec.has_value())
          is_full_sync[id] = ec.value();
        else
          exec_st_.ReportError(ec.error());
      }
    };
    // Lock to prevent the error handler from running instantly
    // while the flows are in a mixed state.
    lock_guard lk{flows_op_mu_};

    shard_set->pool()->AwaitFiberOnAll(std::move(shard_cb));

    size_t num_full_flows =
        std::accumulate(is_full_sync.get(), is_full_sync.get() + num_df_flows, 0);

    if (num_full_flows == num_df_flows) {
      DVLOG(1) << "Calling Flush on all slots " << this;

      if (slot_range_.has_value()) {
        JournalExecutor{&service_}.FlushSlots(slot_range_.value());
      } else {
        JournalExecutor{&service_}.FlushAll();
      }
      DVLOG(1) << "Flush on all slots ended " << this;
    } else if (num_full_flows == 0) {
      sync_type = "partial";
    } else {
      last_journal_LSNs_.reset();
      exec_st_.ReportError(std::make_error_code(errc::state_not_recoverable),
                           "Won't do a partial sync: some flows must fully resync");
    }
  }

  RETURN_ON_ERR(exec_st_.GetError());

  // Send DFLY SYNC.
  if (auto ec = SendNextPhaseRequest("SYNC"); ec) {
    return exec_st_.ReportError(ec);
  }

  LOG(INFO) << "Started " << sync_type << " sync with " << server().Description();

  // Wait for all flows to receive full sync cut.
  // In case of an error, this is unblocked by the error handler.
  VLOG(1) << "Waiting for all full sync cut confirmations";
  sync_block->Wait();

  // Check if we woke up due to cancellation.
  if (!exec_st_.IsRunning())
    return exec_st_.GetError();

  RdbLoader::PerformPostLoad(&service_);

  // Send DFLY STARTSTABLE.
  if (auto ec = SendNextPhaseRequest("STARTSTABLE"); ec) {
    return exec_st_.ReportError(ec);
  }

  // Joining flows and resetting state is done by cleanup.
  double seconds = double(absl::ToInt64Milliseconds(absl::Now() - start_time)) / 1000;
  LOG(INFO) << sync_type << " sync finished in " << strings::HumanReadableElapsedTime(seconds);

  return exec_st_.GetError();
}
```

Each shard has its own **`DflyShardReplica`** object that independently handles data receiving and writing:

`replica.h:173-241`

```cpp
class RdbLoader;
// This class implements a single shard replication flow from a Dragonfly master instance.
// Multiple DflyShardReplica objects are managed by a Replica object.
class DflyShardReplica : public ProtocolClient {
 public:
  DflyShardReplica(ServerContext server_context, MasterContext master_context, uint32_t flow_id,
                   Service* service, std::shared_ptr<MultiShardExecution> multi_shard_exe);
  ~DflyShardReplica();

  void Cancel();
  void JoinFlow();

  // Start replica initialized as dfly flow.
  // Sets is_full_sync when successful.
  io::Result<bool> StartSyncFlow(util::fb2::BlockingCounter block, ExecutionState* cntx,
                                 std::optional<LSN>);

  // Transition into stable state mode as dfly flow.
  std::error_code StartStableSyncFlow(ExecutionState* cntx);

  // Single flow full sync fiber spawned by StartFullSyncFlow.
  void FullSyncDflyFb(std::string eof_token, util::fb2::BlockingCounter block,
                      ExecutionState* cntx);

  // Single flow stable state sync fiber spawned by StartStableSyncFlow.
  void StableSyncDflyReadFb(ExecutionState* cntx);

  void StableSyncDflyAcksFb(ExecutionState* cntx);

  void ExecuteTx(TransactionData&& tx_data, ExecutionState* cntx);

  uint32_t FlowId() const;

  uint64_t JournalExecutedCount() const {
    return journal_rec_executed_.load(std::memory_order_relaxed);
  }

  // Can be called from any thread.
  void Pause(bool pause);

 private:
  Service& service_;
  MasterContext master_context_;

  std::optional<base::IoBuf> leftover_buf_;

  util::fb2::EventCount shard_replica_waker_;  // waker for trans_data_queue_

  std::unique_ptr<JournalExecutor> executor_;
  std::unique_ptr<RdbLoader> rdb_loader_;

  // The master instance has a LSN for each journal record. This counts
  // the number of journal records executed in this flow plus the initial
  // journal offset that we received in the transition from full sync
  // to stable sync.
  // Note: This is not 1-to-1 the LSN in the master, because this counts
  // **executed** records, which might be received interleaved when commands
  // run out-of-order on the master instance.
  // Atomic, because JournalExecutedCount() can be called from any thread.
  std::atomic_uint64_t journal_rec_executed_ = 0;

  util::fb2::Fiber sync_fb_, acks_fb_;
  size_t ack_offs_ = 0;
  int proactor_index_ = -1;
  bool force_ping_ = false;

  std::shared_ptr<MultiShardExecution> multi_shard_exe_;
  uint32_t flow_id_ = UINT32_MAX;  // Flow id if replica acts as a dfly flow.
};
```

This design allows **parallel data transmission and parallel writing** to different shards, fully utilizing multi-core CPUs.

### **2. Batch Async Write Mechanism**

RdbLoader uses efficient batch write strategy instead of writing item by item:

`rdb_load.cc:2661-2737`

```cpp
// loaded at a time. This reduces the memory required to load huge objects and
// prevents LoadItemsBuffer blocking. (Note so far only RDB_TYPE_SET and
// RDB_TYPE_SET_WITH_EXPIRY support partial reads).
error_code RdbLoader::LoadKeyValPair(int type, ObjSettings* settings) {
  std::string key;
  int64_t start = absl::GetCurrentTimeNanos();

  SET_OR_RETURN(ReadKey(), key);

  bool streamed = false;
  do {
    // If there is a cached Item in the free pool, take it, otherwise allocate
    // a new Item (LoadItemsBuffer returns free items).
    Item* item = item_queue_.Pop();
    if (item == nullptr) {
      item = new Item;
    }
    // Delete the item if we fail to load the key/val pair.
    auto cleanup = absl::Cleanup([item] { delete item; });

    item->load_config.append = pending_read_.remaining > 0;

    error_code ec = ReadObj(type, &item->val);
    if (ec) {
      VLOG(2) << "ReadObj error " << ec << " for key " << key;
      return ec;
    }

    // If the key can be discarded, we must still continue to read the
    // object from the RDB so we can read the next key.
    if (ShouldDiscardKey(key, settings)) {
      pending_read_.reserve = 0;
      continue;
    }

    if (pending_read_.remaining > 0) {
      item->key = key;
      streamed = true;
    } else {
      // Avoid copying the key if this is the last read of the object.
      item->key = std::move(key);
    }

    item->load_config.streamed = streamed;
    item->load_config.reserve = pending_read_.reserve;
    // Clear 'reserve' as we must only set when the object is first
    // initialized.
    pending_read_.reserve = 0;

    item->is_sticky = settings->is_sticky;
    item->has_mc_flags = settings->has_mc_flags;
    item->mc_flags = settings->mc_flags;
    item->expire_ms = settings->expiretime;

    std::move(cleanup).Cancel();
    ShardId sid = Shard(item->key, shard_set->size());
    EngineShard* es = EngineShard::tlocal();

    if (es && es->shard_id() == sid) {
      DbContext db_cntx{&namespaces->GetDefaultNamespace(), cur_db_index_, GetCurrentTimeMs()};
      CreateObjectOnShard(db_cntx, item, &db_cntx.GetDbSlice(sid));
      item_queue_.Push(item);
    } else {
      auto& out_buf = shard_buf_[sid];

      out_buf.emplace_back(std::move(item));

      constexpr size_t kBufSize = 64;
      if (out_buf.size() >= kBufSize) {
        // Despite being async, this function can block if the shard queue is full.
        FlushShardAsync(sid);
      }
    }
  } while (pending_read_.remaining > 0);

  int delta_ms = (absl::GetCurrentTimeNanos() - start) / 1000'000;
  LOG_IF(INFO, delta_ms > 1000) << "Took " << delta_ms << " ms to load rdb_type " << type;

  return kOk;
}
```

Key optimization points:

- **Batch Buffering**: Each shard maintains a buffer, batching 64 items before sending
- **Async Dispatch**: Uses **`FlushShardAsync`** to asynchronously dispatch data to target shard
- **Local Optimization**: If already on target shard's thread, writes directly to avoid cross-thread scheduling

    `rdb_load.cc:2497-2525`

    ```cpp
    void RdbLoader::FlushShardAsync(ShardId sid) {
      auto& out_buf = shard_buf_[sid];
      if (out_buf.empty())
        return;

      auto cb = [indx = this->cur_db_index_, this, ib = std::move(out_buf)] {
        auto& db_slice = namespaces->GetDefaultNamespace().GetCurrentDbSlice();

        // Before we start loading, increment LoadInProgress.
        // This is required because FlushShardAsync dispatches to multiple shards, and those shards
        // might have not yet have their state (load in progress) incremented.
        db_slice.IncrLoadInProgress();
        this->LoadItemsBuffer(indx, ib);
        db_slice.DecrLoadInProgress();

        // Block, if tiered storage is active, but can't keep up
        while (db_slice.shard_owner()->ShouldThrottleForTiering()) {
          ThisFiber::SleepFor(100us);
        }
      };

      bool preempted = shard_set->Add(sid, std::move(cb));
      VLOG_IF(2, preempted) << "FlushShardAsync was throttled";
    }

    void RdbLoader::FlushAllShards() {
      for (ShardId i = 0; i < shard_set->size(); i++)
        FlushShardAsync(i);
    }
    ```


### **3. Lock-Free Batch Write Processing**

On target shard, process items in batch to reduce lock contention:

`rdb_load.cc:2623-2646`

```cpp
void RdbLoader::LoadItemsBuffer(DbIndex db_ind, const ItemsBuf& ib) {
  EngineShard* es = EngineShard::tlocal();
  DbContext db_cntx{&namespaces->GetDefaultNamespace(), db_ind, GetCurrentTimeMs()};
  DbSlice& db_slice = db_cntx.GetDbSlice(es->shard_id());

  DCHECK(!db_slice.IsCacheMode());

  bool dry_run = absl::GetFlag(FLAGS_rdb_load_dry_run);

  for (const auto* item : ib) {
    if (dry_run) {
      continue;
    }

    CreateObjectOnShard(db_cntx, item, &db_slice);
    if (stop_early_) {
      return;
    }
  }

  for (auto* item : ib) {
    item_queue_.Push(item);
  }
}
```

Each shard processes data on its own thread, **completely lock-free**, only needing to operate local data structures.

### **4. Item Object Pool Reuse**

Uses object pool to avoid frequent memory allocation:

`rdb_load.h:274-346`

```cpp
struct Item {
    std::string key;
    OpaqueObj val;
    uint64_t expire_ms;
    std::atomic<Item*> next;
    bool is_sticky = false;
    bool has_mc_flags = false;
    uint32_t mc_flags = 0;

    LoadConfig load_config;

    friend void MPSC_intrusive_store_next(Item* dest, Item* nxt) {
      dest->next.store(nxt, std::memory_order_release);
    }

    friend Item* MPSC_intrusive_load_next(const Item& src) {
      return src.next.load(std::memory_order_acquire);
    }
  };

  using ItemsBuf = std::vector<Item*>;

  struct ObjSettings;

  std::error_code LoadKeyValPair(int type, ObjSettings* settings);
  // Returns whether to discard the read key pair.
  bool ShouldDiscardKey(std::string_view key, ObjSettings* settings) const;
  void ResizeDb(size_t key_num, size_t expire_num);
  std::error_code HandleAux();

  DbIndex cur_db_index_ = 0;
  bool pause_ = false;
  bool is_tiered_enabled_ = false;
  AggregateError ec_;

  // We use atomics here because shard threads can notify RdbLoader fiber from another thread
  // that it should stop early.
  std::atomic_bool stop_early_{false};

  // Callback when receiving RDB_OPCODE_FULLSYNC_END
  std::function<void()> full_sync_cut_cb;

  // A free pool of allocated unused items.
  base::MPSCIntrusiveQueue<Item> item_queue_;
};
```

**`item_queue_`** is an MPSC queue used to reuse Item objects, reducing memory allocation overhead.

### **5. Streaming and Partial Loading**

For large objects, supports streaming to avoid loading entire object into memory at once:

`rdb_load.h:116-144`

```cpp
 // Contains the state of a pending partial read.
  //
  // This us used to load huge objects in parts (only loading a subset of
  // elements at a time) (see LoadKeyValPair).
  struct PendingRead {
    // Number of elements in the object to reserve.
    //
    // Used to reserve the elements in a huge object up front, then append
    // in next loads.
    size_t reserve = 0;

    // Number of elements remaining in the object.
    size_t remaining = 0;
  };

  struct LoadConfig {
    // Whether the loaded item is being streamed incrementally in partial
    // reads.
    bool streamed = false;

    // Number of elements in the object to reserve.
    //
    // Used to reserve the elements in a huge object up front, then append
    // in next loads.
    size_t reserve = 0;

    // Whether to append to the existing object or initialize a new object.
    bool append = false;
  };
```

### **6. RDB Pipeline Processing**

During full sync, data is directly read and parsed from socket stream, using **`PrefixSource`** for efficient stream processing:

`replica.cc:801-850`

```cpp
void DflyShardReplica::FullSyncDflyFb(std::string eof_token, BlockingCounter bc,
                                      ExecutionState* cntx) {
  DCHECK(leftover_buf_);
  io::PrefixSource ps{leftover_buf_->InputBuffer(), Sock()};

  rdb_loader_->SetFullSyncCutCb([bc, ran = false]() mutable {
    if (!ran) {
      bc->Dec();
      ran = true;
    }
  });

  // Load incoming rdb stream.
  if (std::error_code ec = rdb_loader_->Load(&ps); ec) {
    cntx->ReportError(ec, "Error loading rdb format");
    return;
  }

  // Try finding eof token.
  io::PrefixSource chained_tail{rdb_loader_->Leftover(), &ps};
  if (!eof_token.empty()) {
    unique_ptr<uint8_t[]> buf{new uint8_t[eof_token.size()]};

    io::Result<size_t> res =
        chained_tail.ReadAtLeast(io::MutableBytes{buf.get(), eof_token.size()}, eof_token.size());

    if (!res || *res != eof_token.size()) {
      cntx->ReportError(std::make_error_code(errc::protocol_error),
                        "Error finding eof token in stream");
      return;
    }
  }

  // Keep loader leftover.
  io::Bytes unused = chained_tail.UnusedPrefix();
  if (unused.size() > 0) {
    leftover_buf_.emplace(unused.size());
    leftover_buf_->WriteAndCommit(unused.data(), unused.size());
  } else {
    leftover_buf_.reset();
  }

  if (auto jo = rdb_loader_->journal_offset(); jo.has_value()) {
    this->journal_rec_executed_.store(*jo);
  } else {
    cntx->ReportError(std::make_error_code(errc::protocol_error),
                      "Error finding journal offset in stream");
  }
  VLOG(1) << "FullSyncDflyFb finished after reading " << rdb_loader_->bytes_read() << " bytes";
}

```

## **Optimization Recommendations for Redis/Redis Cluster**

To simulate Dragonfly's fast writes to Redis, I recommend these strategies:

### **1. Pipeline Batch Writing**

**Most important optimization**: Use Redis Pipeline to batch send commands, avoiding RTT delay per command.

```
Recommended batch size: 100-1000 commands/pipeline (adjust based on network conditions)
```

Don't use synchronous single-command writes, which is the slowest approach.

### **2. Multi-Connection Parallel Writing**

For Redis Cluster:

- **Establish independent connections for each Redis node** (preferably multiple connections)
- **Route data by slot** to different connections
- **Execute pipelines concurrently**, don't wait serially

For Redis standalone:

- Establish **multiple independent connections** (4-16, based on CPU core count)
- Shard data and write concurrently
- Use async I/O framework (like asyncio, tokio)

### **3. Data Preprocessing and Grouping**

Following Dragonfly's approach:

- **Pre-calculate slot/shard**, group data by target node
- **Batch buffer** before sending, avoid scattered sends
- Recommended buffer size: 64-256 commands

### **4. Avoid Blocking Operations**

- Use **async network I/O**, not synchronous blocking mode
- Handle writing and receiving data with **independent threads/coroutines**
- Avoid computation beyond serialization on critical path

### **5. Memory Reuse**

Following Dragonfly's object pool design:

- **Reuse command buffers** and connection objects
- Avoid frequent memory allocation and GC
- Pre-allocate sufficient buffers

### **6. Redis Cluster Specific Optimizations**

- **Pre-fetch slot mapping** to avoid MOVED redirects
- **Use CLUSTER SLOTS** command to get topology
- **Locally cache slot->node mapping** to reduce query overhead
- Handle node failures and redirects (MOVED/ASK)

### **7. Write Mode Selection**

For full sync:

- Consider **disabling AOF** (if Redis supports)
- Use **`RESTORE`** command instead of **`SET`** (if data is in RDB format)
- For large keys, consider using **`RESTORE-ASKING`** to avoid blocking

## **Performance Comparison Points**

Dragonfly's performance advantages come from:

1. **Shared-nothing architecture**: Each shard works independently, no lock contention
2. **Multi-threaded parallel processing**: Fully utilizes multi-core CPU
3. **Efficient memory management**: Direct memory operations, no protocol layer overhead

## **Key Differences and Optimization Priorities**

### **Key Differences**

- Dragonfly is **multi-threaded shared-nothing** architecture, each shard processes on independent thread, completely lock-free
- Redis is **single-threaded model**, all commands execute serially
- Writing to Redis must go through **network protocol** and **command parsing**, adding overhead

### **Optimization Priority Ranking**

1. **Pipeline batch writing** (reduce RTT, 10-100x improvement)
2. **Multi-connection parallelism** (2-8x improvement, depends on connection count)
3. **Data pre-grouping** (reduce network round-trips, 20-50% improvement)
4. **Async I/O** (reduce thread blocking, 30-100% improvement)
5. **Memory reuse** (reduce GC pressure, 10-30% improvement)

Even with all optimizations, writing to Redis will still be slower than Dragonfly's internal writes because:

- Need to go through network protocol serialization/deserialization
- Redis single-threaded model limitation
- Need to consider AOF/RDB persistence overhead

### **Testing Focus Points**

When testing, focus on:

- **Keys written per second** (not just total time)
- **Network bandwidth utilization** (whether bottleneck is reached)
- **Redis CPU usage** (whether single core is saturated)
- **Pipeline depth impact on performance**

## **Summary**

Through in-depth analysis of Dragonfly's full sync mechanism, I discovered these key optimization techniques:

1. Multi-flow parallel replication architecture, fully utilizing multi-core CPU
2. Batch async write mechanism, reducing network round-trips
3. Lock-free batch write processing, avoiding lock contention
4. Item object pool reuse, reducing memory allocation overhead
5. Streaming and partial loading, avoiding excessive memory usage
6. RDB pipeline processing, improving data transmission efficiency

When implementing df2redis, although we cannot fully replicate Dragonfly's internal architecture, we can significantly improve write performance to Redis/Redis Cluster through pipeline batch writing, multi-connection parallelism, data pre-grouping, async I/O, and memory reuse optimization techniques.

The key is to understand that Dragonfly's performance advantages come from its shared-nothing architecture and multi-threaded parallel processing, while writing to Redis must consider network protocol overhead and Redis single-threaded model limitations.
