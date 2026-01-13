# Dragonfly Stream 类型数据同步与主从一致性实现

## 研究背景

在实现 df2redis 时，需要理解 Dragonfly 如何通过分片日志（journal）复制机制来实现 Stream 类型数据的主从同步，以及如何保证数据一致性。通过分析 Dragonfly 源码（`src/server/replica.cc` 和 `src/server/stream_family.cc`），我整理了以下 Stream 复制的完整机制。

## **整体复制架构**

Dragonfly 的复制过程分为三个阶段：

1. **连接和握手阶段**：副本节点与主节点建立连接并交换能力信息
2. **完全同步阶段**：主节点创建数据快照并传输给副本
3. **稳定同步阶段**：主节点持续将日志条目流式传输给副本

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


## **Stream 操作的特殊日志处理**

### **XADD 操作的复制**

对于 **`XADD`** 命令，Dragonfly 实现了特殊的日志记录机制。由于实际添加后分配的 Stream ID 可能与命令中指定的不同，系统会通过 **`AddArgsJournaler`** 结构来确保副本使用准确的 Stream ID：

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

在执行 XADD 操作时，如果涉及到 trim 操作且使用了近似修剪（approximate trimming）或 stream 变为空，系统会使用 MINID 或 MAXLEN 来记录日志，而不是原始命令，以确保精确复制：

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

### **XTRIM 操作的复制**

对于 **`XTRIM`** 命令，当使用近似修剪时，系统也会采用特殊的日志记录策略。如果 stream 变为空，会记录 **`XTRIM ... MAXLEN 0`**；否则，会使用 stream 的新 first_id 记录精确的 MINID：

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

这两个命令都使用 **`CO::NO_AUTOJOURNAL`** 标志，意味着它们手动控制日志记录，而不是自动记录原始命令。

`stream_family.cc:671-673`

```cpp
bool JournalAsMinId(const TrimOpts& opts) {
  return opts.approx || opts.IsMaxLen();
}
```

## **数据一致性保证机制**

### **1. 日志流式传输**

在稳定同步阶段，主节点的每个分片都维护自己的日志，所有写操作都会被记录。**`JournalStreamer`** 负责将这些日志条目缓冲并异步写入目标套接字：

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

### **2. 副本端执行**

副本节点在稳定同步阶段，通过 **`StableSyncDflyReadFb`** 函数持续读取并执行主节点发送的日志条目：

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

每个事务数据都会被顺序执行，并更新已执行的日志记录计数器 **`journal_rec_executed_`**，确保操作的顺序性和一致性。

### **3. 测试验证**

Dragonfly 通过专门的测试来验证 Stream 复制的正确性。例如 **`test_stream_approximate_trimming`** 测试创建 100 个 stream，每个包含 200 个条目，执行近似修剪操作后验证主从数据完全一致：

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

## **实现要点总结**

### **关键机制**

1. **精确 ID 记录**：Stream 操作不直接复制原始命令，而是记录实际执行后的精确状态（如实际分配的 Stream ID、trim 后的确切边界）
2. **分片并行复制**：每个分片独立维护日志并并行复制，提高了复制效率
3. **顺序保证**：通过 LSN（日志序列号）和事务顺序执行机制，确保副本按照与主节点相同的顺序应用操作
4. **原子性**：每个事务作为一个整体被复制和执行，保证了操作的原子性

### **针对 df2redis 的建议**

在实现 df2redis 时，需要注意以下几点：

1. **XADD 命令重写**：不能直接转发原始 XADD 命令，必须使用实际分配的 Stream ID
2. **XTRIM 命令处理**：对于近似修剪（`~` 标志），需要将命令重写为精确的 MINID 或 MAXLEN=0
3. **Journal 解析**：需要正确解析 Journal 流中的 XADD 和 XTRIM 命令，识别重写后的格式
4. **顺序保证**：必须按照 LSN 顺序执行命令，不能打乱顺序

这种设计确保了即使在高并发和复杂 trim 操作的情况下，副本节点也能完全复现主节点的 Stream 数据状态，实现强一致性。

## 总结

通过深入分析 Dragonfly 的 Stream 复制机制，我发现了以下关键点：

1. Stream 操作使用特殊的日志记录机制，不直接记录原始命令
2. XADD 和 XTRIM 会被重写为精确的 ID 和边界，确保副本一致性
3. 日志流式传输和顺序执行机制保证了数据的一致性和原子性
4. Dragonfly 通过测试验证了 Stream 复制的正确性

这些发现对于正确实现 df2redis 的增量同步功能至关重要，特别是在处理 Stream 类型数据时。
