# Dragonfly Replica 实现详解

我将为你详细介绍 Dragonfly 的 Replica 复制协议实现，包括全量同步和增量同步的完整流程。

## **一、整体架构**

Dragonfly 支持两种复制模式：

1. **从 Redis Master 复制**（Redis 协议兼容）
2. **从 Dragonfly Master 复制**（原生多分片协议）

主要类结构： 

`replica.h:40-51`

```cpp
// This class manages replication from both Dragonfly and Redis masters.
class Replica : ProtocolClient {
 private:
  // The flow is : R_ENABLED -> R_TCP_CONNECTED -> (R_SYNCING) -> R_SYNC_OK.
  // SYNCING means that the initial ack succeeded. It may be optional if we can still load from
  // the journal offset.
  enum State : unsigned {
    R_ENABLED = 1,  // Replication mode is enabled. Serves for signaling shutdown.
    R_TCP_CONNECTED = 2,
    R_GREETED = 4,
    R_SYNCING = 8,
    R_SYNC_OK = 0x10,
  };
```

## **二、主复制流程状态机**

Replica 的主流程在 **`MainReplicationFb`** 中实现，状态转换如下：

```
R_ENABLED → R_TCP_CONNECTED → R_GREETED → R_SYNCING → R_SYNC_OK → 稳定同步
```

`replica.cc:186-271`

```cpp
void Replica::MainReplicationFb() {
  VLOG(1) << "Main replication fiber started " << this;
  // Switch shard states to replication.
  SetShardStates(true);

  error_code ec;
  while (state_mask_.load() & R_ENABLED) {
    // Discard all previous errors and set default error handler.
    exec_st_.Reset([this](const GenericError& ge) { this->DefaultErrorHandler(ge); });
    // 1. Connect socket.
    if ((state_mask_.load() & R_TCP_CONNECTED) == 0) {
      ThisFiber::SleepFor(500ms);
      if (is_paused_)
        continue;

      ec = ResolveHostDns();
      if (ec) {
        LOG(ERROR) << "Error resolving dns to " << server().host << " " << ec;
        continue;
      }

      // Give a lower timeout for connect, because we're
      reconnect_count_++;
      ec = ConnectAndAuth(absl::GetFlag(FLAGS_master_reconnect_timeout_ms) * 1ms, &exec_st_);
      if (ec) {
        LOG(WARNING) << "Error connecting to " << server().Description() << " " << ec;
        continue;
      }
      VLOG(1) << "Replica socket connected";
      state_mask_.fetch_or(R_TCP_CONNECTED);
      continue;
    }

    // 2. Greet.
    if ((state_mask_.load() & R_GREETED) == 0) {
      ec = Greet();
      if (ec) {
        LOG(INFO) << "Error greeting " << server().Description() << " " << ec << " "
                  << ec.message();
        state_mask_.fetch_and(R_ENABLED);
        continue;
      }
      state_mask_.fetch_or(R_GREETED);
      continue;
    }

    // 3. Initiate full sync
    if ((state_mask_.load() & R_SYNC_OK) == 0) {
      if (HasDflyMaster())
        ec = InitiateDflySync();
      else
        ec = InitiatePSync();

      if (ec) {
        LOG(WARNING) << "Error syncing with " << server().Description() << " " << ec << " "
                     << ec.message();
        state_mask_.fetch_and(R_ENABLED);  // reset all flags besides R_ENABLED
        continue;
      }
      state_mask_.fetch_or(R_SYNC_OK);
      continue;
    }

    // 4. Start stable state sync.
    DCHECK(state_mask_.load() & R_SYNC_OK);

    if (HasDflyMaster())
      ec = ConsumeDflyStream();
    else
      ec = ConsumeRedisStream();

    auto state = state_mask_.fetch_and(R_ENABLED);
    if (state & R_ENABLED) {  // replication was not stopped.
      LOG(WARNING) << "Error stable sync with " << server().Description() << " " << ec << " "
                   << ec.message();
    }
  }

  // Wait for unblocking cleanup to finish.
  exec_st_.JoinErrorHandler();

  // Revert shard states to normal state.
  SetShardStates(false);

  VLOG(1) << "Main replication fiber finished";
}
```

## **三、协议握手阶段（Greet）**

### **3.1 PING 命令**

首先发送 PING 检查连接： 

`replica.cc:277-278`

```cpp
  // Corresponds to server.repl_state == REPL_STATE_CONNECTING state in redis
  RETURN_ON_ERR(SendCommandAndReadResponse("PING"));  // optional.
  PC_RETURN_ON_BAD_RESPONSE(CheckRespIsSimpleReply("PONG"));
```

### **3.2 REPLCONF 配置交换**

需要依次发送以下 REPLCONF 命令：

**1) 发送监听端口：** 

`replica.cc:281-286`

```cpp
  // Corresponds to server.repl_state == REPL_STATE_SEND_HANDSHAKE condition in replication.c
  uint16_t port = absl::GetFlag(FLAGS_announce_port);
  if (port == 0) {
    port = static_cast<uint16_t>(absl::GetFlag(FLAGS_port));
  }
  RETURN_ON_ERR(SendCommandAndReadResponse(StrCat("REPLCONF listening-port ", port)));
  PC_RETURN_ON_BAD_RESPONSE(CheckRespIsSimpleReply("OK"));
```

**2) 发送 IP 地址（可选）：** 

`replica.cc:288-293`

```cpp
  auto announce_ip = absl::GetFlag(FLAGS_replica_announce_ip);
  if (!announce_ip.empty()) {
    RETURN_ON_ERR(SendCommandAndReadResponse(StrCat("REPLCONF ip-address ", announce_ip)));
    LOG_IF(WARNING, !CheckRespIsSimpleReply("OK"))
        << "Master did not OK announced IP address, perhaps it is using an old version";
  }
```

**3) 声明能力（capa）：** 

`replica.cc:296-297`

```cpp
  // Corresponds to server.repl_state == REPL_STATE_SEND_CAPA
  RETURN_ON_ERR(SendCommandAndReadResponse("REPLCONF capa eof capa psync2"));
  PC_RETURN_ON_BAD_RESPONSE(CheckRespIsSimpleReply("OK"));
```

**4) 声明 Dragonfly 客户端：** 

`replica.cc:300-302`

```cpp
  // Announce that we are the dragonfly client.
  // Note that we currently do not support dragonfly->redis replication.
  RETURN_ON_ERR(SendCommandAndReadResponse("REPLCONF capa dragonfly"));
  PC_RETURN_ON_BAD_RESPONSE(CheckRespFirstTypes({RespExpr::STRING}));
```

### **3.3 Dragonfly Master 特殊配置**

如果 Master 返回多个参数（≥3个），说明是 Dragonfly Master： 

`replica.cc:306-312`

```cpp
  } else if (LastResponseArgs().size() >= 3) {  // it's dragonfly master.
    PC_RETURN_ON_BAD_RESPONSE(!HandleCapaDflyResp());
    if (auto ec = ConfigureDflyMaster(); ec)
      return ec;
  } else {
    PC_RETURN_ON_BAD_RESPONSE(false);
  }
```

响应格式解析： 

`replica.cc:318-358`

```cpp
std::error_code Replica::HandleCapaDflyResp() {
  // Response is: <master_repl_id, syncid, num_shards [, version]>
  if (!CheckRespFirstTypes({RespExpr::STRING, RespExpr::STRING, RespExpr::INT64}) ||
      LastResponseArgs()[0].GetBuf().size() != CONFIG_RUN_ID_SIZE)
    return make_error_code(errc::bad_message);

  int64 param_num_flows = get<int64_t>(LastResponseArgs()[2].u);
  if (param_num_flows <= 0 || param_num_flows > 1024) {
    // sanity check, we support upto 1024 shards.
    // It's not that we can not support more but it's probably highly unlikely that someone
    // will run dragonfly with more than 1024 cores.
    LOG(ERROR) << "Invalid flow count " << param_num_flows;
    return make_error_code(errc::bad_message);
  }

  // If we're syncing a different replication ID, drop the saved LSNs.
  string_view master_repl_id = ToSV(LastResponseArgs()[0].GetBuf());
  if (master_context_.master_repl_id != master_repl_id) {
    if (absl::GetFlag(FLAGS_break_replication_on_master_restart) &&
        !master_context_.master_repl_id.empty()) {
      LOG(ERROR) << "Encountered different master repl id (" << master_repl_id << " vs "
                 << master_context_.master_repl_id << ")";
      state_mask_.store(0);
      return make_error_code(errc::connection_aborted);
    }
    last_journal_LSNs_.reset();
  }
  master_context_.master_repl_id = master_repl_id;
  master_context_.dfly_session_id = ToSV(LastResponseArgs()[1].GetBuf());
  master_context_.num_flows = param_num_flows;

  if (LastResponseArgs().size() >= 4) {
    PC_RETURN_ON_BAD_RESPONSE(LastResponseArgs()[3].type == RespExpr::INT64);
    master_context_.version = DflyVersion(get<int64_t>(LastResponseArgs()[3].u));
  }
  VLOG(1) << "Master id: " << master_context_.master_repl_id
          << ", sync id: " << master_context_.dfly_session_id
          << ", num journals: " << param_num_flows
          << ", version: " << unsigned(master_context_.version);

  return error_code{};
}
```

Master 返回：**`<master_repl_id, syncid, num_shards [, version]>`**

随后需要发送客户端 ID 和版本： 

`replica.cc:361-375`

```cpp
std::error_code Replica::ConfigureDflyMaster() {
  // We need to send this because we may require to use this for cluster commands.
  // this reason to send this here is that in other context we can get an error reply
  // since we are budy with the replication
  RETURN_ON_ERR(SendCommandAndReadResponse(StrCat("REPLCONF CLIENT-ID ", id_)));
  if (!CheckRespIsSimpleReply("OK")) {
    LOG(WARNING) << "Bad REPLCONF CLIENT-ID response";
  }

  RETURN_ON_ERR(
      SendCommandAndReadResponse(StrCat("REPLCONF CLIENT-VERSION ", DflyVersion::CURRENT_VER)));
  PC_RETURN_ON_BAD_RESPONSE(CheckRespIsSimpleReply("OK"));

  return error_code{};
}
```

## **四、全量同步阶段**

### **4.1 Redis 协议全量同步（PSYNC）**

**发送 PSYNC 命令：** 

`replica.cc:377-394`

```cpp
error_code Replica::InitiatePSync() {
  base::IoBuf io_buf{128};

  // Corresponds to server.repl_state == REPL_STATE_SEND_PSYNC
  string id("?");  // corresponds to null master id and null offset
  int64_t offs = -1;
  if (!master_context_.master_repl_id.empty()) {  // in case we synced before
    id = master_context_.master_repl_id;          // provide the replication offset and master id
    // TBD: for incremental sync send repl_offs_, not supported yet.
    // offs = repl_offs_;
  }

  RETURN_ON_ERR(SendCommand(StrCat("PSYNC ", id, " ", offs)));

  // Master may delay sync response with "repl_diskless_sync_delay"
  PSyncResponse repl_header;

  RETURN_ON_ERR(ParseReplicationHeader(&io_buf, &repl_header));
```

格式：**`PSYNC <master_repl_id> <offset>`**

- 首次同步：**`PSYNC ? -1`**
- 重新连接：**`PSYNC <saved_master_id> <saved_offset>`**

**解析响应头：** 

`replica.cc:1015-1091`

```cpp
error_code Replica::ParseReplicationHeader(base::IoBuf* io_buf, PSyncResponse* dest) {
  std::string_view str;

  RETURN_ON_ERR(ReadLine(io_buf, &str));

  DCHECK(!str.empty());

  std::string_view header;
  bool valid = false;

  auto bad_header = [str]() {
    LOG(ERROR) << "Bad replication header: " << str;
    return std::make_error_code(std::errc::illegal_byte_sequence);
  };

  // non-empty lines
  if (str[0] != '+') {
    return bad_header();
  }

  header = str.substr(1);
  VLOG(1) << "header: " << header;
  if (absl::ConsumePrefix(&header, "FULLRESYNC ")) {
    // +FULLRESYNC db7bd45bf68ae9b1acac33acb 123\r\n
    //             master_id  repl_offset
    size_t pos = header.find(' ');
    if (pos != std::string_view::npos) {
      if (absl::SimpleAtoi(header.substr(pos + 1), &repl_offs_)) {
        master_context_.master_repl_id = string(header.substr(0, pos));
        valid = true;
        VLOG(1) << "master repl_id " << master_context_.master_repl_id << " / " << repl_offs_;
      }
    }

    if (!valid)
      return bad_header();

    io_buf->ConsumeInput(str.size() + 2);
    RETURN_ON_ERR(ReadLine(io_buf, &str));  // Read the next line parsed below.

    // Readline checks for non ws character first before searching for eol
    // so str must be non empty.
    DCHECK(!str.empty());

    if (str[0] != '$') {
      return bad_header();
    }

    std::string_view token = str.substr(1);
    VLOG(1) << "token: " << token;
    if (absl::ConsumePrefix(&token, "EOF:")) {
      CHECK_EQ(kRdbEofMarkSize, token.size()) << token;
      dest->fullsync.emplace<string>(token);
      VLOG(1) << "Token: " << token;
    } else {
      size_t rdb_size = 0;
      if (!absl::SimpleAtoi(token, &rdb_size))
        return std::make_error_code(std::errc::illegal_byte_sequence);

      VLOG(1) << "rdb size " << rdb_size;
      dest->fullsync.emplace<size_t>(rdb_size);
    }
    io_buf->ConsumeInput(str.size() + 2);
  } else if (absl::ConsumePrefix(&header, "CONTINUE")) {
    // we send psync2 so we should get master replid.
    // That could change due to redis failovers.
    // TODO: part sync
    dest->fullsync.emplace<size_t>(0);
    LOG(ERROR) << "Partial replication not supported yet";
    return std::make_error_code(std::errc::not_supported);
  } else {
    LOG(ERROR) << "Unknown replication header";
    return bad_header();
  }

  return error_code{};
}
```

响应格式：

1. **`+FULLRESYNC <master_id> <offset>\r\n`**
2. 然后是 RDB 数据大小或 EOF token：
    - **`$<size>\r\n`** - 表示 RDB 大小（disk-based）
    - **`$EOF:<40字符token>\r\n`** - 表示 diskless 模式

**加载 RDB 数据：** 

`replica.cc:405-454`

```cpp
  // we get token for diskless redis replication. For disk based replication
  // we get the snapshot size.
  if (snapshot_size || token != nullptr) {
    LOG(INFO) << "Starting full sync with Redis master";

    state_mask_.fetch_or(R_SYNCING);

    io::PrefixSource ps{io_buf.InputBuffer(), Sock()};

    // Set LOADING state.
    if (!service_.RequestLoadingState()) {
      return exec_st_.ReportError(std::make_error_code(errc::state_not_recoverable),
                                  "Failed to enter LOADING state");
    }

    absl::Cleanup cleanup = [this]() { service_.RemoveLoadingState(); };

    if (slot_range_.has_value()) {
      JournalExecutor{&service_}.FlushSlots(slot_range_.value());
    } else {
      JournalExecutor{&service_}.FlushAll();
    }

    RdbLoader loader(NULL);
    loader.SetLoadUnownedSlots(true);
    loader.set_source_limit(snapshot_size);
    // TODO: to allow registering callbacks within loader to send '\n' pings back to master.
    // Also to allow updating last_io_time_.
    error_code ec = loader.Load(&ps);
    RETURN_ON_ERR(ec);
    VLOG(1) << "full sync completed";

    if (token) {
      uint8_t buf[kRdbEofMarkSize];
      io::PrefixSource chained(loader.Leftover(), &ps);
      VLOG(1) << "Before reading from chained stream";
      io::Result<size_t> eof_res = chained.Read(io::MutableBytes{buf});
      CHECK(eof_res && *eof_res == kRdbEofMarkSize);

      VLOG(1) << "Comparing token " << ToSV(buf);

      // TODO: handle gracefully...
      CHECK_EQ(0, memcmp(token->data(), buf, kRdbEofMarkSize));
      CHECK(chained.UnusedPrefix().empty());
    } else {
      CHECK_EQ(0u, loader.Leftover().size());
      CHECK_EQ(snapshot_size, loader.bytes_read());
    }

    CHECK(ps.UnusedPrefix().empty());
    io_buf.ConsumeInput(io_buf.InputLen());
    TouchIoTime();
```

关键点：

- 进入 LOADING 状态
- 清空数据（FlushAll 或 FlushSlots）
- 使用 **`RdbLoader`** 加载 RDB 数据
- Diskless 模式需要验证 EOF token（40字节）

**完成全量同步：** 

`replica.cc:459-467`

```cpp
  state_mask_.fetch_and(~R_SYNCING);
  state_mask_.fetch_or(R_SYNC_OK);

  // There is a data race condition in Redis-master code, where "ACK 0" handler may be
  // triggered before Redis is ready to transition to the streaming state and it silenty ignores
  // "ACK 0". We reduce the chance it happens with this delay.
  ThisFiber::SleepFor(50ms);

  return error_code{};
```

注意：有个 50ms 延迟避免 Redis Master 的竞态条件。

### **4.2 Dragonfly 协议全量同步**

Dragonfly 使用多流（multi-flow）架构，每个 shard 一个独立的复制流：

**初始化流：** 

`replica.cc:471-503`

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
```

**启动每个 shard 的同步流：** 

`replica.cc:522-565`

```cpp
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
```

**单个流的全量同步协议：**

1. 发送 FLOW 命令： 
    
    `replica.cc:737-784`
    
    ```cpp
    io::Result<bool> DflyShardReplica::StartSyncFlow(BlockingCounter sb, ExecutionState* cntx,
                                                     std::optional<LSN> lsn) {
      using nonstd::make_unexpected;
      DCHECK(!master_context_.master_repl_id.empty() && !master_context_.dfly_session_id.empty());
      proactor_index_ = ProactorBase::me()->GetPoolIndex();
    
      RETURN_ON_ERR_T(make_unexpected,
                      ConnectAndAuth(absl::GetFlag(FLAGS_master_connect_timeout_ms) * 1ms, &exec_st_));
    
      VLOG(1) << "Sending on flow " << master_context_.master_repl_id << " "
              << master_context_.dfly_session_id << " " << flow_id_;
    
      std::string cmd = StrCat("DFLY FLOW ", master_context_.master_repl_id, " ",
                               master_context_.dfly_session_id, " ", flow_id_);
      // Try to negotiate a partial sync if possible.
      if (lsn.has_value() && master_context_.version > DflyVersion::VER1 &&
          absl::GetFlag(FLAGS_replica_partial_sync)) {
        absl::StrAppend(&cmd, " ", *lsn);
      }
    
      ResetParser(RedisParser::Mode::CLIENT);
      leftover_buf_.emplace(128);
      RETURN_ON_ERR_T(make_unexpected, SendCommand(cmd));
      auto read_resp = ReadRespReply(&*leftover_buf_);
      if (!read_resp.has_value()) {
        return make_unexpected(read_resp.error());
      }
    
      PC_RETURN_ON_BAD_RESPONSE_T(make_unexpected,
                                  CheckRespFirstTypes({RespExpr::STRING, RespExpr::STRING}));
    
      string_view flow_directive = ToSV(LastResponseArgs()[0].GetBuf());
      string eof_token;
      PC_RETURN_ON_BAD_RESPONSE_T(make_unexpected,
                                  flow_directive == "FULL" || flow_directive == "PARTIAL");
      bool is_full_sync = flow_directive == "FULL";
    
      eof_token = ToSV(LastResponseArgs()[1].GetBuf());
    
      leftover_buf_->ConsumeInput(read_resp->left_in_buffer);
    
      // We can not discard io_buf because it may contain data
      // besides the response we parsed. Therefore we pass it further to ReplicateDFFb.
      sync_fb_ = fb2::Fiber("shard_full_sync", &DflyShardReplica::FullSyncDflyFb, this,
                            std::move(eof_token), sb, cntx);
    
      return is_full_sync;
    }
    ```
    
    格式：**`DFLY FLOW <master_repl_id> <session_id> <flow_id> [<lsn>]`**
    
    响应：**`["FULL"|"PARTIAL", "<eof_token>"]`**
    
2. 加载 RDB 数据： 
    
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
    
3. 发送 SYNC 命令： 
    
    `replica.cc:568-573`
    
    ```cpp
      // Send DFLY SYNC.
      if (auto ec = SendNextPhaseRequest("SYNC"); ec) {
        return exec_st_.ReportError(ec);
      }
    ```
    
4. 发送 STARTSTABLE 命令： 
    
    `replica.cc:586-590`
    
    ```cpp
      // Send DFLY STARTSTABLE.
      if (auto ec = SendNextPhaseRequest("STARTSTABLE"); ec) {
        return exec_st_.ReportError(ec);
      }
    ```
    

## **五、增量同步阶段（Stable Sync）**

### **5.1 Redis 协议增量同步**

**开始接收命令流：** 

`replica.cc:599-669`

```cpp
error_code Replica::ConsumeRedisStream() {
  base::IoBuf io_buf(16_KB);
  ConnectionContext conn_context{nullptr, {}};
  conn_context.is_replicating = true;
  conn_context.journal_emulated = true;
  conn_context.skip_acl_validation = true;
  conn_context.ns = &namespaces->GetDefaultNamespace();

  // we never reply back on the commands.
  facade::CapturingReplyBuilder null_builder{facade::ReplyMode::NONE};
  ResetParser(RedisParser::Mode::SERVER);

  // Master waits for this command in order to start sending replication stream.
  RETURN_ON_ERR(SendCommand("REPLCONF ACK 0"));

  VLOG(1) << "Before reading repl-log";

  // Redis sends either pings every "repl_ping_slave_period" time inside replicationCron().
  // or, alternatively, write commands stream coming from propagate() function.
  // Replica connection must send "REPLCONF ACK xxx" in order to make sure that master replication
  // buffer gets disposed of already processed commands, this is done in a separate fiber.
  error_code ec;
  LOG(INFO) << "Transitioned into stable sync";

  // Set new error handler.
  auto err_handler = [this](const auto& ge) {
    // Trigger ack-fiber
    replica_waker_.notifyAll();
    DefaultErrorHandler(ge);
  };
  RETURN_ON_ERR(exec_st_.SwitchErrorHandler(std::move(err_handler)));

  facade::CmdArgVec args_vector;

  acks_fb_ = fb2::Fiber("redis_acks", &Replica::RedisStreamAcksFb, this);

  while (true) {
    auto response = ReadRespReply(&io_buf, /*copy_msg=*/false);
    if (!response.has_value()) {
      VLOG(1) << "ConsumeRedisStream finished";
      exec_st_.ReportError(response.error());
      acks_fb_.JoinIfNeeded();
      return response.error();
    }

    if (!LastResponseArgs().empty()) {
      string cmd = absl::CHexEscape(ToSV(LastResponseArgs()[0].GetBuf()));

      // Valkey and Redis may send MULTI and EXEC as part of their replication commands.
      // Dragonfly disallows some commands, such as SELECT, inside of MULTI/EXEC, so here we simply
      // ignore MULTI/EXEC and execute their inner commands individually.
      if (!absl::EqualsIgnoreCase(cmd, "MULTI") && !absl::EqualsIgnoreCase(cmd, "EXEC")) {
        VLOG(2) << "Got command " << cmd << "\n consumed: " << response->total_read;

        if (LastResponseArgs()[0].GetBuf()[0] == '\r') {
          for (const auto& arg : LastResponseArgs()) {
            LOG(INFO) << absl::CHexEscape(ToSV(arg.GetBuf()));
          }
        }

        facade::RespExpr::VecToArgList(LastResponseArgs(), &args_vector);
        CmdArgList arg_list{args_vector.data(), args_vector.size()};
        service_.DispatchCommand(arg_list, &null_builder, &conn_context);
      }
    }

    io_buf.ConsumeInput(response->left_in_buffer);
    repl_offs_ += response->total_read;
    replica_waker_.notify();  // Notify to trigger ACKs.
  }
}
```

关键流程：

1. 发送 **`REPLCONF ACK 0`** 启动复制流
2. 持续读取 RESP 协议的命令
3. 忽略 MULTI/EXEC 命令
4. 使用 **`JournalExecutor`** 执行命令
5. 更新 **`repl_offs_`** 偏移量

**ACK 确认机制：** 

`replica.cc:887-908`

```cpp
void Replica::RedisStreamAcksFb() {
  constexpr size_t kAckRecordMaxInterval = 1024;
  std::chrono::duration ack_time_max_interval =
      1ms * absl::GetFlag(FLAGS_replication_acks_interval);
  std::string ack_cmd;
  auto next_ack_tp = std::chrono::steady_clock::now();

  while (exec_st_.IsRunning()) {
    VLOG(2) << "Sending an ACK with offset=" << repl_offs_;
    ack_cmd = absl::StrCat("REPLCONF ACK ", repl_offs_);
    next_ack_tp = std::chrono::steady_clock::now() + ack_time_max_interval;
    if (auto ec = SendCommand(ack_cmd); ec) {
      exec_st_.ReportError(ec);
      break;
    }
    ack_offs_ = repl_offs_;

    replica_waker_.await_until(
        [&]() { return repl_offs_ > ack_offs_ + kAckRecordMaxInterval || (!exec_st_.IsRunning()); },
        next_ack_tp);
  }
}
```

定期发送：**`REPLCONF ACK <offset>`**

- 时间间隔：由 **`FLAGS_replication_acks_interval`** 控制（默认 1000ms）
- 或者达到一定数据量（1024 字节）

### **5.2 Dragonfly 协议增量同步**

**转入稳定同步：** 

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

**读取 Journal 流：** 

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

使用 **`TransactionReader`** 读取 Journal 条目： 

`tx_executor.cc:92-117`

```cpp
std::optional<TransactionData> TransactionReader::NextTxData(JournalReader* reader,
                                                             ExecutionState* cntx) {
  if (!cntx->IsRunning()) {
    return std::nullopt;
  }
  io::Result<journal::ParsedEntry> res;
  if (res = reader->ReadEntry(); !res) {
    cntx->ReportError(res.error());
    return std::nullopt;
  }

  // When LSN opcode is sent master does not increase journal lsn.
  if (lsn_.has_value() && res->opcode != journal::Op::LSN) {
    ++*lsn_;
    VLOG(2) << "read lsn: " << *lsn_;
  }

  TransactionData tx_data = TransactionData::FromEntry(std::move(res.value()));
  if (lsn_.has_value() && tx_data.opcode == journal::Op::LSN) {
    DCHECK_NE(tx_data.lsn, 0u);
    LOG_IF_EVERY_N(WARNING, tx_data.lsn != *lsn_, 10000)
        << "master lsn:" << tx_data.lsn << " replica lsn" << *lsn_;
    DCHECK_EQ(tx_data.lsn, *lsn_);
  }
  return tx_data;
}
```

**执行事务：** 

`replica.cc:961-1013`

```cpp
void DflyShardReplica::ExecuteTx(TransactionData&& tx_data, ExecutionState* cntx) {
  if (!cntx->IsRunning()) {
    return;
  }

  if (!tx_data.IsGlobalCmd()) {
    VLOG(3) << "Execute cmd without sync between shards. txid: " << tx_data.txid;
    executor_->Execute(tx_data.dbid, tx_data.command);
    return;
  }

  bool inserted_by_me =
      multi_shard_exe_->InsertTxToSharedMap(tx_data.txid, master_context_.num_flows);

  auto& multi_shard_data = multi_shard_exe_->Find(tx_data.txid);

  VLOG(2) << "Execute txid: " << tx_data.txid << " waiting for data in all shards";
  // Wait until shards flows got transaction data and inserted to map.
  // This step enforces that replica will execute multi shard commands that finished on master
  // and replica recieved all the commands from all shards.
  multi_shard_data.block->Wait();
  // Check if we woke up due to cancellation.
  if (!exec_st_.IsRunning())
    return;
  VLOG(2) << "Execute txid: " << tx_data.txid << " block wait finished";

  VLOG(2) << "Execute txid: " << tx_data.txid << " global command execution";
  // Wait until all shards flows get to execution step of this transaction.
  multi_shard_data.barrier.Wait();
  // Check if we woke up due to cancellation.
  if (!exec_st_.IsRunning())
    return;
  // Global command will be executed only from one flow fiber. This ensure corectness of data in
  // replica.
  if (inserted_by_me) {
    executor_->Execute(tx_data.dbid, tx_data.command);
  }
  // Wait until exection is done, to make sure we done execute next commands while the global is
  // executed.
  multi_shard_data.barrier.Wait();
  // Check if we woke up due to cancellation.
  if (!exec_st_.IsRunning())
    return;

  // Erase from map can be done only after all flow fibers executed the transaction commands.
  // The last fiber which will decrease the counter to 0 will be the one to erase the data from
  // map
  auto val = multi_shard_data.counter.fetch_sub(1, std::memory_order_relaxed);
  VLOG(2) << "txid: " << tx_data.txid << " counter: " << val;
  if (val == 1) {
    multi_shard_exe_->Erase(tx_data.txid);
  }
}
```

**ACK 机制：** 

`replica.cc:910-942`

```cpp
void DflyShardReplica::StableSyncDflyAcksFb(ExecutionState* cntx) {
  DCHECK_EQ(proactor_index_, ProactorBase::me()->GetPoolIndex());

  constexpr size_t kAckRecordMaxInterval = 1024;
  std::chrono::duration ack_time_max_interval =
      1ms * absl::GetFlag(FLAGS_replication_acks_interval);
  std::string ack_cmd;
  auto next_ack_tp = std::chrono::steady_clock::now();

  uint64_t current_offset;
  while (cntx->IsRunning()) {
    // Handle ACKs with the master. PING opcodes from the master mean we should immediately
    // answer.
    current_offset = journal_rec_executed_.load(std::memory_order_relaxed);
    VLOG(1) << "Sending an ACK with offset=" << current_offset << " forced=" << force_ping_;
    ack_cmd = absl::StrCat("REPLCONF ACK ", current_offset);
    force_ping_ = false;
    next_ack_tp = std::chrono::steady_clock::now() + ack_time_max_interval;
    if (auto ec = SendCommand(ack_cmd); ec) {
      cntx->ReportError(ec);
      break;
    }
    ack_offs_ = current_offset;

    shard_replica_waker_.await_until(
        [&]() {
          return journal_rec_executed_.load(std::memory_order_relaxed) >
                     ack_offs_ + kAckRecordMaxInterval ||
                 force_ping_ || (!cntx->IsRunning());
        },
        next_ack_tp);
  }
}
```

## **六、Journal 格式**

Journal 操作类型定义： 

`types.h:16`

```cpp
enum class Op : uint8_t { NOOP = 0, SELECT = 6, EXPIRED = 9, COMMAND = 10, PING = 13, LSN = 15 };
```

Journal Entry 结构： 

`types.h:18-70`

```cpp
struct EntryBase {
  TxId txid;
  Op opcode;
  DbIndex dbid;
  uint32_t shard_cnt;  // This field is no longer used by the replica, but we continue to serialize
                       // and deserialize it to maintain backward compatibility.
  std::optional<SlotId> slot;
  LSN lsn{0};
};

// This struct represents a single journal entry.
// Those are either control instructions or commands.
struct Entry : public EntryBase {
  // Payload represents a non-owning view into a command executed on the shard.
  struct Payload {
    std::string_view cmd;
    std::variant<ShardArgs,  // Shard parts.
                 ArgSlice>   // Parts of a full command.
        args;

    Payload() = default;

    Payload(std::string_view c, const ShardArgs& a) : cmd(c), args(a) {
    }
    Payload(std::string_view c, ArgSlice a) : cmd(c), args(a) {
    }
  };

  Entry(TxId txid, Op opcode, DbIndex dbid, uint32_t shard_cnt, std::optional<SlotId> slot_id,
        Payload pl)
      : EntryBase{txid, opcode, dbid, shard_cnt, slot_id}, payload{pl} {
  }

  Entry(journal::Op opcode, DbIndex dbid, std::optional<SlotId> slot_id)
      : EntryBase{0, opcode, dbid, 0, slot_id, 0} {
  }

  Entry(journal::Op opcode, LSN lsn) : EntryBase{0, opcode, 0, 0, std::nullopt, lsn} {
  }

  Entry(TxId txid, journal::Op opcode, DbIndex dbid, uint32_t shard_cnt,
        std::optional<SlotId> slot_id)
      : EntryBase{txid, opcode, dbid, shard_cnt, slot_id, 0} {
  }

  bool HasPayload() const {
    return !payload.cmd.empty();
  }

  std::string ToString() const;

  Payload payload;
};
```

## **七、关键实现细节**

### **7.1 EOF Token**

- 长度：40 字节
- 用于 diskless 复制标记 RDB 结束
    
    `replica.cc:63`
    
    ```cpp
    constexpr unsigned kRdbEofMarkSize = 40;
    ```
    

### **7.2 部分同步支持**

Dragonfly 支持部分同步，保存 LSN： 

`replica.h:152-154`

```cpp
  // A vector of the last executer LSNs when a replication is interrupted.
  // Allows partial sync on reconnects.
  std::optional<std::vector<LSN>> last_journal_LSNs_;
```

重连时尝试从上次的 LSN 继续： 

`replica.cc:752-755`

```cpp
io::Result<bool> DflyShardReplica::StartSyncFlow(BlockingCounter sb, ExecutionState* cntx,
                                                 std::optional<LSN> lsn) {
  using nonstd::make_unexpected;
  DCHECK(!master_context_.master_repl_id.empty() && !master_context_.dfly_session_id.empty());
  proactor_index_ = ProactorBase::me()->GetPoolIndex();

  RETURN_ON_ERR_T(make_unexpected,
                  ConnectAndAuth(absl::GetFlag(FLAGS_master_connect_timeout_ms) * 1ms, &exec_st_));

  VLOG(1) << "Sending on flow " << master_context_.master_repl_id << " "
          << master_context_.dfly_session_id << " " << flow_id_;

  std::string cmd = StrCat("DFLY FLOW ", master_context_.master_repl_id, " ",
                           master_context_.dfly_session_id, " ", flow_id_);
  // Try to negotiate a partial sync if possible.
  if (lsn.has_value() && master_context_.version > DflyVersion::VER1 &&
      absl::GetFlag(FLAGS_replica_partial_sync)) {
    absl::StrAppend(&cmd, " ", *lsn);
  }

  ResetParser(RedisParser::Mode::CLIENT);
  leftover_buf_.emplace(128);
  RETURN_ON_ERR_T(make_unexpected, SendCommand(cmd));
  auto read_resp = ReadRespReply(&*leftover_buf_);
  if (!read_resp.has_value()) {
    return make_unexpected(read_resp.error());
  }

  PC_RETURN_ON_BAD_RESPONSE_T(make_unexpected,
                              CheckRespFirstTypes({RespExpr::STRING, RespExpr::STRING}));

  string_view flow_directive = ToSV(LastResponseArgs()[0].GetBuf());
  string eof_token;
  PC_RETURN_ON_BAD_RESPONSE_T(make_unexpected,
                              flow_directive == "FULL" || flow_directive == "PARTIAL");
  bool is_full_sync = flow_directive == "FULL";

  eof_token = ToSV(LastResponseArgs()[1].GetBuf());

  leftover_buf_->ConsumeInput(read_resp->left_in_buffer);

  // We can not discard io_buf because it may contain data
  // besides the response we parsed. Therefore we pass it further to ReplicateDFFb.
  sync_fb_ = fb2::Fiber("shard_full_sync", &DflyShardReplica::FullSyncDflyFb, this,
                        std::move(eof_token), sb, cntx);

  return is_full_sync;
}
```

### **7.3 多分片事务同步**

对于跨分片的全局命令（如 FLUSHDB），需要特殊同步： 

`tx_executor.cc:70-84`

```cpp
bool TransactionData::IsGlobalCmd() const {
  if (command.cmd_args.empty()) {
    return false;
  }

  auto& args = command.cmd_args;
  if (absl::EqualsIgnoreCase(ToSV(args[0]), "FLUSHDB"sv) ||
      absl::EqualsIgnoreCase(ToSV(args[0]), "FLUSHALL"sv) ||
      (absl::EqualsIgnoreCase(ToSV(args[0]), "DFLYCLUSTER"sv) &&
       absl::EqualsIgnoreCase(ToSV(args[1]), "FLUSHSLOTS"sv))) {
    return true;
  }

  return false;
}
```

### **7.4 错误处理**

主流程使用 **`ExecutionState`** 管理错误： 

`replica.cc:94-108`

```cpp
  auto check_connection_error = [this](error_code ec, const char* msg) -> GenericError {
    if (!exec_st_.IsRunning()) {
      return {"replication cancelled"};
    }
    if (ec) {
      exec_st_.ReportCancelError();
      return {absl::StrCat(msg, ec.message())};
    }
    return ec;
  };

  // 0. Set basic error handler that is reponsible for cleaning up on errors.
  // Can return an error only if replication was cancelled immediately.
  auto err = exec_st_.SwitchErrorHandler([this](const auto& ge) { this->DefaultErrorHandler(ge); });
  RETURN_ON_GENERIC_ERR(check_connection_error(err, "replication cancelled"));
```

## **八、实现建议**

基于以上分析，实现 Dragonfly 到 Redis 的同步需要注意：

1. **握手阶段**：严格按顺序发送 PING → REPLCONF listening-port → REPLCONF capa → REPLCONF capa dragonfly
2. **全量同步**：
    - 发送 **`PSYNC ? -1`**
    - 正确解析 **`+FULLRESYNC`** 响应
    - 处理两种 RDB 格式（size-based 和 EOF token）
    - 验证 EOF token（如果是 diskless）
3. **增量同步**：
    - 发送 **`REPLCONF ACK 0`** 启动
    - 持续读取 RESP 命令
    - 定期发送 ACK
    - 正确处理 MULTI/EXEC
4. **连接管理**：
    - 支持自动重连
    - 保存 master_repl_id 和 offset
    - 实现超时机制
5. **状态管理**：使用状态机管理连接状态，避免状态混乱

## **Notes**

- Dragonfly 的原生协议使用多流架构，每个 shard 独立连接，这与 Redis 单连接模式不同
- 对于 Redis 目标，只需要实现 Redis 协议部分（PSYNC 流程）
- EOF token 是随机生成的 40 字节十六进制字符串，用于标记 diskless 复制的结束
- ACK 机制对于 Master 清理复制缓冲区很重要，必须正确实现
- Dragonfly 会自动忽略 MULTI/EXEC 包装，因为它不支持在 MULTI 块内的某些命令
- 部分同步功能可以大幅减少重连开销，建议保存 LSN 信息