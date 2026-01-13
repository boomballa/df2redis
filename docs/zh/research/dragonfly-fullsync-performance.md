# Dragonfly 全量同步的高效写入机制详解

## 研究背景

在实现 df2redis 时，我需要理解为什么 Dragonfly 的全量同步非常快，并且如何将这些优化应用到向 Redis/Redis Cluster 的写入中。通过分析 Dragonfly 源码（`src/server/replica.cc` 和 `src/server/rdb_load.cc`），我整理了以下关键的架构设计和优化技术。

## **核心优化技术**

Dragonfly 的全量同步之所以非常快，主要得益于以下几个关键的架构设计和优化技术：

### **1. 多流并行复制架构 (Multi-flow Replication)**

Dragonfly 使用多条独立的连接流来并行传输数据，每个 shard 对应一个独立的复制流：

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

每个 shard 都有自己的 **`DflyShardReplica`** 对象，独立处理数据接收和写入：

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

这种设计让数据可以**并行传输和并行写入**到不同的 shard，充分利用多核 CPU。

### **2. 批量异步写入机制**

RdbLoader 使用了高效的批量写入策略，而不是逐条写入：

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

关键优化点：

- **批量缓冲**：每个 shard 维护一个缓冲区，累积 64 个 item 后才批量发送
- **异步分发**：使用 **`FlushShardAsync`** 异步将数据分发到目标 shard
- **本地优化**：如果当前就在目标 shard 的线程上，直接写入，避免跨线程调度

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


### **3. 无锁的批量写入处理**

在目标 shard 上，批量处理 items，减少锁竞争：

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

每个 shard 在自己的线程上处理数据，**完全无锁**，只需要在本地操作数据结构。

### **4. Item 对象池复用**

使用对象池避免频繁的内存分配：

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

**`item_queue_`** 是一个 MPSC 队列，用于复用 Item 对象，减少内存分配开销。

### **5. 流式传输和部分加载**

对于大对象，支持流式传输，避免一次性加载整个对象到内存：

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

### **6. RDB 流水线处理**

全量同步时，数据直接从 socket 流式读取并解析，使用 **`PrefixSource`** 进行高效的流处理：

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

## **针对 Redis/Redis Cluster 的优化建议**

要模拟 Dragonfly 的快速写入到 Redis，我建议采用以下策略：

### **1. Pipeline 批量写入**

**最重要的优化**：使用 Redis Pipeline 批量发送命令，避免每个命令的 RTT 延迟。

```
建议批量大小：100-1000 条命令/pipeline（根据网络情况调整）
```

不要使用同步的逐条写入，这是最慢的方式。

### **2. 多连接并行写入**

针对 Redis Cluster：

- **为每个 Redis 节点建立独立连接**（最好是多条连接）
- **根据 slot 分发数据**到不同的连接
- **并发执行 pipeline**，不要串行等待

针对单实例 Redis：

- 建立**多条独立连接**（4-16条，根据 CPU 核心数）
- 将数据分片后并发写入
- 使用异步 I/O 框架（如 asyncio、tokio）

### **3. 数据预处理和分组**

参考 Dragonfly 的做法：

- **提前计算 slot/shard**，将数据按目标节点分组
- **批量缓冲**后再发送，避免零散发送
- 缓冲区大小建议：64-256 条命令

### **4. 避免阻塞操作**

- 使用**异步网络 I/O**，不要用同步阻塞模式
- 写入和接收数据用**独立的线程/协程**处理
- 避免在关键路径上做序列化以外的计算

### **5. 内存复用**

参考 Dragonfly 的对象池设计：

- **复用命令缓冲区**和连接对象
- 避免频繁的内存分配和 GC
- 预分配足够的缓冲区

### **6. 针对 Redis Cluster 的特殊优化**

- **提前获取 slot 映射**，避免 MOVED 重定向
- **使用 CLUSTER SLOTS** 命令获取拓扑
- **本地缓存 slot->node 映射**，减少查询开销
- 处理节点故障和重定向（MOVED/ASK）

### **7. 写入模式选择**

对于全量同步：

- 可以考虑**禁用 AOF**（如果 Redis 支持）
- 使用 **`RESTORE`** 命令代替 **`SET`**（如果数据是 RDB 格式）
- 对于大 key，考虑使用 **`RESTORE-ASKING`** 避免阻塞

## **性能对比要点**

Dragonfly 的性能优势来自：

1. **Shared-nothing 架构**：每个 shard 独立工作，无锁竞争
2. **多线程并行处理**：充分利用多核 CPU
3. **高效的内存管理**：直接操作内存，无需经过协议层

## **关键差异与优化重点**

### **关键差异**

- Dragonfly 是**多线程共享 nothing** 架构，每个 shard 在独立线程上处理，完全无锁
- Redis 是**单线程模型**，所有命令串行执行
- 向 Redis 写入时，必须通过**网络协议**和**命令解析**，有额外开销

### **优化重点排序**

1. **Pipeline 批量写入**（减少 RTT，提升 10-100 倍）
2. **多连接并行**（提升 2-8 倍，取决于连接数）
3. **数据预分组**（减少网络往返，提升 20-50%）
4. **异步 I/O**（减少线程阻塞，提升 30-100%）
5. **内存复用**（减少 GC 压力，提升 10-30%）

即使做了所有优化，向 Redis 写入仍然会比 Dragonfly 内部写入慢，因为：

- 需要经过网络协议序列化/反序列化
- Redis 单线程模型的限制
- 需要考虑 AOF/RDB 持久化开销

### **测试关注点**

建议测试时关注：

- **每秒写入的 key 数量**（不仅仅是总时间）
- **网络带宽利用率**（是否达到瓶颈）
- **Redis CPU 使用率**（是否单核打满）
- **Pipeline 深度对性能的影响**

## 总结

通过深入分析 Dragonfly 的全量同步机制，我发现了以下关键优化技术：

1. 多流并行复制架构，充分利用多核 CPU
2. 批量异步写入机制，减少网络往返
3. 无锁的批量写入处理，避免锁竞争
4. Item 对象池复用，减少内存分配开销
5. 流式传输和部分加载，避免内存占用过大
6. RDB 流水线处理，提高数据传输效率

在实现 df2redis 时，虽然无法完全复制 Dragonfly 的内部架构，但可以通过 Pipeline 批量写入、多连接并行、数据预分组、异步 I/O 和内存复用等优化手段，显著提升向 Redis/Redis Cluster 的写入性能。

关键是要理解 Dragonfly 的性能优势来自于其 shared-nothing 架构和多线程并行处理，而向 Redis 写入时需要考虑网络协议开销和 Redis 单线程模型的限制。
