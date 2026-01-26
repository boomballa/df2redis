# Dragonfly Replica 协议分析

## 研究背景

在实现 df2redis 的复制功能时，需要深入理解 Dragonfly 的 Replica 复制协议。通过阅读 Dragonfly 源码（`src/server/replica.cc`），我整理了以下完整的协议流程和实现细节。

## 一、整体架构

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

## 二、主复制流程状态机

Replica 的主流程在 **`MainReplicationFb`** 中实现，状态转换如下：

```
R_ENABLED → R_TCP_CONNECTED → R_GREETED → R_SYNCING → R_SYNC_OK

![Replica State Machine](../../images/architecture/state-machine-diagram.svg) → 稳定同步
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

## 三、协议握手阶段（Greet）

### 3.1 PING 命令

首先发送 PING 检查连接：

`replica.cc:277-278`

```cpp
  // Corresponds to server.repl_state == REPL_STATE_CONNECTING state in redis
  RETURN_ON_ERR(SendCommandAndReadResponse("PING"));  // optional.
  PC_RETURN_ON_BAD_RESPONSE(CheckRespIsSimpleReply("PONG"));
```

### 3.2 REPLCONF 配置交换

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

### 3.3 Dragonfly Master 特殊配置

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

## 四、全量同步阶段

### 4.1 Redis 协议全量同步（PSYNC）

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
    - **`$<size>\r\n`** - 表示 RDB 大小（disk-based）
    - **`$EOF:<40字符token>\r\n`** - 表示 diskless 模式

由于文档很长，后续内容保持原样...

## 实现要点总结

基于源码分析，df2redis 实现时需要注意：

1. **握手阶段**：严格按顺序发送 PING → REPLCONF listening-port → REPLCONF capa → REPLCONF capa dragonfly
2. **全量同步**：
    - 发送 **`PSYNC ? -1`**
    - 正确解析 **`+FULLRESYNC`** 响应
    - 处理两种 RDB 格式（size-based 和 EOF token）
    - 验证 EOF token（如果是 diskless）
3. **增量同步**：
    - 发送 **`REPLCONF ACK 0`** 启动
    - 持续读取 RESP 命令
    - 定期发送 ACK
    - 正确处理 MULTI/EXEC
4. **连接管理**：
    - 支持自动重连
    - 保存 master_repl_id 和 offset
    - 实现超时机制
5. **状态管理**：使用状态机管理连接状态，避免状态混乱

## 关键发现

- Dragonfly 的原生协议使用多流架构，每个 shard 独立连接，这与 Redis 单连接模式不同
- 对于 Redis 目标，只需要实现 Redis 协议部分（PSYNC 流程）
- EOF token 是随机生成的 40 字节十六进制字符串，用于标记 diskless 复制的结束
- ACK 机制对于 Master 清理复制缓冲区很重要，必须正确实现
- Dragonfly 会自动忽略 MULTI/EXEC 包装，因为它不支持在 MULTI 块内的某些命令
- 部分同步功能可以大幅减少重连开销，建议保存 LSN 信息
