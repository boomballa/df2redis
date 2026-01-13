# Dragonfly Stream Type Data Synchronization and Replication Consistency

## Research Context

When implementing df2redis, I needed to understand how Dragonfly achieves master-replica synchronization for Stream type data through its sharded journal replication mechanism, and how it guarantees data consistency. By analyzing Dragonfly source code (`src/server/replica.cc` and `src/server/stream_family.cc`), I documented the complete Stream replication mechanism below.

## **Overall Replication Architecture**

Dragonfly's replication process consists of three phases:

1. **Connection and Handshake Phase**: Replica establishes connection with master and exchanges capability information
2. **Full Sync Phase**: Master creates data snapshot and transmits to replica
3. **Stable Sync Phase**: Master continuously streams journal entries to replica

    `replica.cc:671-713`

    ```cpp
    error_code Replica::ConsumeDflyStream() {
      // Set new error handler that closes flow sockets.
      auto err_handler = [this](const auto& ge) {
        // Make sure the flows are not in a state transition
        lock_guard lk{flows_op_mu_};
        DefaultErrorHandler(ge);
        for (auto& flow : shard_flows_) {
          flow->Cancel();
        }
        multi_shard_exe_->CancelAllBlockingEntities();
      };
      RETURN_ON_ERR(exec_st_.SwitchErrorHandler(std::move(err_handler)));

      LOG(INFO) << "Transitioned into stable sync";
      // Transition flows into stable sync.
      {
        auto shard_cb = [&](unsigned index, auto*) {
          const auto& local_ids = thread_flow_map_[index];
          for (unsigned id : local_ids) {
            auto ec = shard_flows_[id]->StartStableSyncFlow(&exec_st_);
            if (ec)
              exec_st_.ReportError(ec);
          }
        };

        // Lock to prevent error handler from running on mixed state.
        lock_guard lk{flows_op_mu_};
        shard_set->pool()->AwaitFiberOnAll(std::move(shard_cb));
      }

      JoinDflyFlows();

      last_journal_LSNs_.emplace();
      for (auto& flow : shard_flows_) {
        last_journal_LSNs_->push_back(flow->JournalExecutedCount());
      }

      LOG(INFO) << "Exit stable sync";
      // The only option to unblock is to cancel the context.
      CHECK(exec_st_.GetError());

      return exec_st_.GetError();
    }
    ```


## **Special Journal Handling for Stream Operations**

### **XADD Operation Replication**

For **`XADD`** command, Dragonfly implements a special journaling mechanism. Since the actual Stream ID assigned after adding may differ from the one specified in the command, the system uses **`AddArgsJournaler`** structure to ensure replica uses the exact Stream ID:

`stream_family.cc:113-123`

```cpp
/* Used to journal the XADD command.
   The actual stream ID assigned after adding may differ from the one specified in the command.
   So, for the replica, we need to specify the exact ID that was actually added. */
struct AddArgsJournaler {
  void SetStreamId(std::string_view stream_id) {
    add_args[stream_id_index] = stream_id;
  }

  CmdArgVec add_args;
  size_t stream_id_index;
};
```

When executing XADD, if trim operation is involved and approximate trimming is used or stream becomes empty, the system journals using MINID or MAXLEN instead of the original command to ensure exact replication:

`stream_family.cc:728-765`

```cpp
  if (op_args.shard->journal()) {
    std::string result_id_as_string = StreamsIdToString(result_id);
    const bool stream_is_empty = stream_inst->length == 0;

    if (opts.trim_opts && (stream_is_empty || JournalAsMinId(opts.trim_opts.value()))) {
      std::string last_id;

      CmdArgVec journal_args = {key};
      journal_args.reserve(args.size() + 4);

      if (stream_is_empty) {
        // We need remove the whole stream in replica
        journal_args.emplace_back("MAXLEN"sv);
        journal_args.emplace_back("0"sv);
      } else {
        // We need to set exact MinId in the journal.
        // For this we are using new first_id from the stream
        last_id = StreamsIdToString(stream_inst->first_id);
        journal_args.emplace_back("MINID"sv);
        journal_args.emplace_back(last_id);
      }

      if (opts.no_mkstream) {
        journal_args.emplace_back("NOMKSTREAM"sv);
      }

      journal_args.emplace_back(result_id_as_string);

      for (size_t i = 0; i < args.size(); i++) {
        journal_args.emplace_back(args[i]);
      }

      RecordJournal(op_args, "XADD"sv, journal_args);
    } else {
      journaler.SetStreamId(result_id_as_string);
      RecordJournal(op_args, "XADD"sv, journaler.add_args);
    }
  }
```

### **XTRIM Operation Replication**

For **`XTRIM`** command, when approximate trimming is used, the system also adopts a special journaling strategy. If stream becomes empty, it journals **`XTRIM ... MAXLEN 0`**; otherwise, it uses the stream's new first_id to journal exact MINID:

`stream_family.cc:2056-2067`

```cpp
  if (op_args.shard->journal() && journal_as_minid) {
    const bool stream_is_empty = s->length == 0;
    if (stream_is_empty) {
      // We need remove the whole stream in replica
      RecordJournal(op_args, "XTRIM"sv, ArgSlice{key, "MAXLEN"sv, "0"sv});
    } else {
      // We need to set exact MinId in the journal.
      // For this we are using new first_id from the stream
      std::string last_id = StreamsIdToString(s->first_id);
      RecordJournal(op_args, "XTRIM"sv, ArgSlice{key, "MINID"sv, last_id});
    }
  }
```

Both commands use **`CO::NO_AUTOJOURNAL`** flag, meaning they manually control journaling instead of automatically recording original commands.

`stream_family.cc:671-673`

```cpp
bool JournalAsMinId(const TrimOpts& opts) {
  return opts.approx || opts.IsMaxLen();
}
```

## **Data Consistency Guarantee Mechanisms**

### **1. Journal Streaming**

During stable sync phase, each shard on master maintains its own journal, with all write operations recorded. **`JournalStreamer`** is responsible for buffering these journal entries and asynchronously writing them to the destination socket:

`streamer.h:19-73`

```cpp
// Buffered single-shard journal streamer that listens for journal changes with a
// journal listener and writes them to a destination sink in a separate fiber.
class JournalStreamer {
 public:
  JournalStreamer(journal::Journal* journal, ExecutionState* cntx);
  virtual ~JournalStreamer();

  // Self referential.
  JournalStreamer(const JournalStreamer& other) = delete;
  JournalStreamer(JournalStreamer&& other) = delete;

  // Register journal listener and start writer in fiber.
  virtual void Start(util::FiberSocketBase* dest, bool send_lsn);

  // Must be called on context cancellation for unblocking
  // and manual cleanup.
  virtual void Cancel();

  size_t UsedBytes() const;

 protected:
  // TODO: we copy the string on each write because JournalItem may be passed to multiple
  // streamers so we can not move it. However, if we would either wrap JournalItem in shared_ptr
  // or wrap JournalItem::data in shared_ptr, we can avoid the cost of copying strings.
  // Also, for small strings it's more peformant to copy to the intermediate buffer than
  // to issue an io operation.
  void Write(std::string str);

  // Blocks the if the consumer if not keeping up.
  void ThrottleIfNeeded();

  virtual bool ShouldWrite(const journal::JournalItem& item) const {
    return cntx_->IsRunning();
  }

  void WaitForInflightToComplete();

  util::FiberSocketBase* dest_ = nullptr;
  ExecutionState* cntx_;

 private:
  void AsyncWrite();
  void OnCompletion(std::error_code ec, size_t len);

  bool IsStalled() const;

  journal::Journal* journal_;

  PendingBuf pending_buf_;

  size_t in_flight_bytes_ = 0, total_sent_ = 0;
  time_t last_lsn_time_ = 0;
  util::fb2::EventCount waker_;
  uint32_t journal_cb_id_{0};
};
```

### **2. Replica-Side Execution**

During stable sync phase, replica continuously reads and executes journal entries sent by master through **`StableSyncDflyReadFb`** function:

`replica.cc:852-885`

```cpp
void DflyShardReplica::StableSyncDflyReadFb(ExecutionState* cntx) {
  DCHECK_EQ(proactor_index_, ProactorBase::me()->GetPoolIndex());

  // Check leftover from full sync.
  io::Bytes prefix{};
  if (leftover_buf_ && leftover_buf_->InputLen() > 0) {
    prefix = leftover_buf_->InputBuffer();
  }

  io::PrefixSource ps{prefix, Sock()};

  JournalReader reader{&ps, 0};
  DCHECK_GE(journal_rec_executed_, 1u);
  TransactionReader tx_reader{journal_rec_executed_.load(std::memory_order_relaxed) - 1};

  acks_fb_ = fb2::Fiber("shard_acks", &DflyShardReplica::StableSyncDflyAcksFb, this, cntx);

  std::optional<TransactionData> tx_data;
  while ((tx_data = tx_reader.NextTxData(&reader, cntx))) {
    DVLOG(3) << "Lsn: " << tx_data->lsn;

    last_io_time_ = Proactor()->GetMonotonicTimeNs();
    if (tx_data->opcode == journal::Op::LSN) {
      //  Do nothing
    } else if (tx_data->opcode == journal::Op::PING) {
      force_ping_ = true;
      journal_rec_executed_.fetch_add(1, std::memory_order_relaxed);
    } else {
      ExecuteTx(std::move(*tx_data), cntx);
      journal_rec_executed_.fetch_add(1, std::memory_order_relaxed);
    }
    shard_replica_waker_.notifyAll();
  }
}
```

Each transaction data is executed in order, updating the executed journal record counter **`journal_rec_executed_`** to ensure operation ordering and consistency.

### **3. Test Validation**

Dragonfly validates Stream replication correctness through dedicated tests. For example, **`test_stream_approximate_trimming`** test creates 100 streams each containing 200 entries, executes approximate trimming, and verifies master-replica data is completely consistent:

`replication_test.py:2834-2877`

```cpp
@dfly_args({"proactor_threads": 4})
async def test_stream_approximate_trimming(df_factory):
    master = df_factory.create()
    replica = df_factory.create()

    df_factory.start_all([master, replica])
    c_master = master.client()
    c_replica = replica.client()

    await c_replica.execute_command(f"REPLICAOF localhost {master.port}")
    await wait_for_replicas_state(c_replica)

    # Step 1: Populate master with 100 streams, each containing 200 entries
    num_streams = 100
    entries_per_stream = 200

    for i in range(num_streams):
        stream_name = f"stream{i}"
        for j in range(entries_per_stream):
            await c_master.execute_command("XADD", stream_name, "*", f"field{j}", f"value{j}")

    # Step 2: Trim each stream to a random size between 70 and 200
    for i in range(num_streams):
        stream_name = f"stream{i}"
        trim_size = random.randint(70, entries_per_stream)
        await c_master.execute_command("XTRIM", stream_name, "MAXLEN", "~", trim_size)

    # Wait for replica sync
    await asyncio.sleep(1)

    # Check replica data consistent
    master_data = await DebugPopulateSeeder.capture(c_master)
    replica_data = await DebugPopulateSeeder.capture(c_replica)
    assert master_data == replica_data

    # Step 3: Trim all streams to 0
    for i in range(num_streams):
        stream_name = f"stream{i}"
        await c_master.execute_command("XTRIM", stream_name, "MAXLEN", "0")

    # Check replica data consistent
    master_data = await DebugPopulateSeeder.capture(c_master)
    replica_data = await DebugPopulateSeeder.capture(c_replica)
    assert master_data == replica_data
```

## **Implementation Highlights**

### **Key Mechanisms**

1. **Exact ID Recording**: Stream operations don't directly replicate original commands, but record exact state after execution (like actual assigned Stream ID, exact boundaries after trim)
2. **Sharded Parallel Replication**: Each shard independently maintains journal and replicates in parallel, improving replication efficiency
3. **Ordering Guarantee**: Through LSN (Log Sequence Number) and transaction sequential execution mechanism, ensures replica applies operations in same order as master
4. **Atomicity**: Each transaction is replicated and executed as a whole, guaranteeing operation atomicity

### **Recommendations for df2redis**

When implementing df2redis, pay attention to these points:

1. **XADD Command Rewriting**: Cannot directly forward original XADD command, must use actual assigned Stream ID
2. **XTRIM Command Handling**: For approximate trimming (`~` flag), need to rewrite command as exact MINID or MAXLEN=0
3. **Journal Parsing**: Need to correctly parse XADD and XTRIM commands in Journal stream, recognize rewritten format
4. **Ordering Guarantee**: Must execute commands in LSN order, cannot shuffle order

This design ensures that even under high concurrency and complex trim operations, replica can completely reproduce master's Stream data state, achieving strong consistency.

## **Summary**

Through in-depth analysis of Dragonfly's Stream replication mechanism, I discovered these key points:

1. Stream operations use special journaling mechanism, not directly recording original commands
2. XADD and XTRIM are rewritten as exact IDs and boundaries to ensure replica consistency
3. Journal streaming and sequential execution mechanisms guarantee data consistency and atomicity
4. Dragonfly validates Stream replication correctness through tests

These findings are critical for correctly implementing incremental sync functionality in df2redis, especially when handling Stream type data.
