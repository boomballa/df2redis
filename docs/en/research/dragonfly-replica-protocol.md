# Dragonfly Replication Protocol Implementation Analysis

## Research Context

When implementing the replication functionality for df2redis, I needed to thoroughly understand Dragonfly's Replica replication protocol. By reading through Dragonfly source code (`src/server/replica.cc`), I documented the complete protocol flow and implementation details below.

## **1. Replica State Machine**

Dragonfly Replica's state machine is defined as follows:

`replica.h:60-67`

```cpp
  enum State : unsigned {
    R_ENABLED = 1,         // Replication mode is enabled. Serves for signalling shutdown.
    R_TCP_CONNECTED = 2,   // Tcp connection established.
    R_GREETED = 4,         // Greeted.
    R_SYNCING = 8,         // Sync in progress - full or stable state.
    R_SYNC_OK = 0x10,      // Sync finished
  };
```

The typical state transition flow:

```
R_ENABLED → R_TCP_CONNECTED → R_GREETED → R_SYNCING → R_SYNC_OK
```

Core execution flow reference:

`replica.cc:234-349`

```cpp
void Replica::MainReplicationFb() {
  VLOG(1) << "Main replication fiber started";
  ThisFiber::SetName("MainReplication");

  // ... error handling setup ...

  error_code ec = ConnectAndAuth(master_context_.master_ep, &exec_st_.io_cntx);
  if (!ec) {
    ec = Greet();
  }
  if (!ec) {
    ec = InitiateDflySync();  // Full sync stage
  }
  if (!ec) {
    ec = ConsumeRedisStream();  // Stable sync stage
  }

  // ... cleanup ...
}
```

## **2. Handshake Protocol**

### **2.1 TCP Connection Establishment**

Dragonfly establishes TCP connection via standard Linux socket API:

`replica.cc:406-439`

```cpp
error_code Replica::ConnectAndAuth(string_view host, io::Context* cntx) {
  // ... resolve host ...

  auto address = *result.begin();
  auto& io_cntx = *cntx;

  error_code ec = sock_->Connect(address, &io_cntx);
  if (ec) {
    return ec;
  }

  // ... authentication ...

  if (master_context_.master_auth) {
    ReqSerializer serializer{sock_.get()};
    ec = serializer.SendCommand(ArgSlice{"AUTH", *master_context_.master_auth});
    if (ec) return ec;

    auto payload = ReadRespReply(&io_cntx, sock_.get());
    // ... check response ...
  }

  state_mask_.fetch_or(R_TCP_CONNECTED);
  return kOk;
}
```

### **2.2 Capability Exchange (REPLCONF)**

After connection, replica exchanges capability info with master via `REPLCONF` commands:

`replica.cc:611-642`

```cpp
error_code Replica::Greet() {
  ReqSerializer serializer{sock_.get()};
  string id = service_.server_family().master_replid();

  // Send PING command
  error_code ec = serializer.SendCommand("PING");
  if (ec) return ec;

  // Consume PING response
  RETURN_ON_ERR(CheckRespIsSimpleReply("PONG"));

  // Send REPLCONF listening-port
  RETURN_ON_ERR(SendCommand(absl::StrCat("REPLCONF listening-port ", master_context_.master_ep.port)));
  RETURN_ON_ERR(CheckRespIsSimpleReply("OK"));

  // Send REPLCONF capa dragonfly
  RETURN_ON_ERR(SendCommand("REPLCONF capa dragonfly"));

  // ... parse master response, get DFLOW count ...

  state_mask_.fetch_or(R_GREETED);
  return kOk;
}
```

**Key capability: `capa dragonfly`**

This command signals to master that replica supports Dragonfly's FLOW protocol. Master's response includes the number of FLOWs (typically equal to shard count).

## **3. Full Sync Phase**

### **3.1 FLOW Registration**

For Dragonfly master, replica needs to establish multiple FLOW connections (one per shard) before requesting data:

`replica.cc:471-597`

```cpp
error_code Replica::InitiateDflySync() {
  // Initialize shard flows
  shard_flows_.resize(master_context_.num_flows);
  for (unsigned i = 0; i < shard_flows_.size(); ++i) {
    shard_flows_[i].reset(
        new DflyShardReplica(server(), master_context_, i, &service_, multi_shard_exe_));
  }

  // Start full sync flows
  state_mask_.fetch_or(R_SYNCING);

  // Each flow establishes connection and starts receiving RDB
  auto shard_cb = [&](unsigned index, auto*) {
    for (auto id : thread_flow_map_[index]) {
      shard_flows_[id]->StartSyncFlow(sync_block, &exec_st_, ...);
    }
  };
  shard_set->pool()->AwaitFiberOnAll(std::move(shard_cb));

  // Send DFLY SYNC command
  if (auto ec = SendNextPhaseRequest("SYNC"); ec) {
    return exec_st_.ReportError(ec);
  }

  // Wait for all flows to finish full sync
  sync_block->Wait();

  return exec_st_.GetError();
}
```

Each FLOW's connection establishment:

`dfly_shard_replica.cc:164-251`

```cpp
io::Result<bool> DflyShardReplica::StartSyncFlow(BlockingCounter block, ExecutionState* cntx,
                                                 std::optional<LSN> lsn) {
  // Establish FLOW connection
  auto ec = ConnectAndAuth(master_context_.master_ep, cntx);
  if (ec) return make_unexpected(ec);

  // Register FLOW via DFLY FLOW command
  ec = SendCommand(absl::StrCat("DFLY FLOW ", flow_id_, " ", version_));
  if (ec) return make_unexpected(ec);

  // Parse FLOW response to get sync type
  // ...

  // Start receiving RDB stream
  sync_fb_ = MakeFiber([this, eof_token = std::move(eof_token), block, cntx]() mutable {
    FullSyncDflyFb(std::move(eof_token), block, cntx);
  });

  return true;
}
```

### **3.2 Requesting Data (PSYNC/DFLY SYNC)**

- For Redis master: Use `PSYNC ? -1` to request full sync
- For Dragonfly master: Use `DFLY SYNC` command after establishing all FLOWs

After master receives the sync request, it starts transmitting RDB data. Each FLOW receives one shard's RDB data.

### **3.3 Receiving and Parsing RDB**

Each FLOW receives RDB stream in a separate fiber:

`dfly_shard_replica.cc:801-850`

```cpp
void DflyShardReplica::FullSyncDflyFb(std::string eof_token, BlockingCounter bc,
                                      ExecutionState* cntx) {
  io::PrefixSource ps{leftover_buf_->InputBuffer(), Sock()};

  // Load RDB stream
  if (std::error_code ec = rdb_loader_->Load(&ps); ec) {
    cntx->ReportError(ec, "Error loading rdb format");
    return;
  }

  // Find EOF token (40-byte SHA1 checksum)
  if (!eof_token.empty()) {
    unique_ptr<uint8_t[]> buf{new uint8_t[eof_token.size()]};
    io::Result<size_t> res = chained_tail.ReadAtLeast(..., eof_token.size());
    // ... verify EOF token ...
  }

  // Save journal offset for incremental sync
  if (auto jo = rdb_loader_->journal_offset(); jo.has_value()) {
    this->journal_rec_executed_.store(*jo);
  }
}
```

**Important**: Dragonfly uses diskless replication, appending a 40-byte EOF token after RDB data. This token must be consumed before entering stable sync.

## **4. Global Synchronization Barrier**

One of Dragonfly's key innovations is the **global barrier mechanism** to prevent data loss during phase transition:

`replica.cc:112-128`

```cpp
error_code Replica::SendNextPhaseRequest(string_view command) {
  VLOG(1) << "SendNextPhaseRequest: " << command;

  // Wait for all flows to reach sync point
  for (auto& flow : shard_flows_) {
    flow->WaitUntilPaused();
  }

  // Send DFLY STARTSTABLE command
  ReqSerializer serializer{sock_.get()};
  error_code ec = serializer.SendCommand(ArgSlice{absl::StrCat("DFLY ", command)});

  // Resume all flows
  for (auto& flow : shard_flows_) {
    flow->Pause(false);
  }

  return ec;
}
```

**Why barrier is needed:**

- Each FLOW receives RDB data at different speeds
- Some FLOWs might finish loading RDB while others are still processing
- Without barrier, journal streaming might miss writes that occurred during RDB phase
- Barrier ensures all FLOWs complete RDB phase before master starts sending journal entries

## **5. Stable Sync Phase (Incremental Sync)**

After barrier sync, replica sends `DFLY STARTSTABLE` command. Master then starts streaming journal entries:

`replica.cc:671-713`

```cpp
error_code Replica::ConsumeDflyStream() {
  // Transition all flows into stable sync
  {
    auto shard_cb = [&](unsigned index, auto*) {
      for (unsigned id : thread_flow_map_[index]) {
        auto ec = shard_flows_[id]->StartStableSyncFlow(&exec_st_);
        if (ec)
          exec_st_.ReportError(ec);
      }
    };
    shard_set->pool()->AwaitFiberOnAll(std::move(shard_cb));
  }

  // Each flow continuously reads and executes journal entries
  JoinDflyFlows();

  return exec_st_.GetError();
}
```

Each FLOW's stable sync logic:

`dfly_shard_replica.cc:852-885`

```cpp
void DflyShardReplica::StableSyncDflyReadFb(ExecutionState* cntx) {
  io::PrefixSource ps{prefix, Sock()};
  JournalReader reader{&ps, 0};
  TransactionReader tx_reader{journal_rec_executed_.load(...) - 1};

  // Continuously read journal entries
  std::optional<TransactionData> tx_data;
  while ((tx_data = tx_reader.NextTxData(&reader, cntx))) {
    if (tx_data->opcode == journal::Op::LSN) {
      // LSN marker, skip
    } else if (tx_data->opcode == journal::Op::PING) {
      // Heartbeat
      journal_rec_executed_.fetch_add(1, ...);
    } else {
      // Execute transaction
      ExecuteTx(std::move(*tx_data), cntx);
      journal_rec_executed_.fetch_add(1, ...);
    }
  }
}
```

### **Journal Entry Format**

Journal entries use packed uint encoding:

`journal_reader.cc:86-133`

```cpp
io::Result<JournalReader::ReadResult> JournalReader::ReadEntry() {
  // Read opcode
  SET_OR_RETURN(ReadUInt(), opcode);

  if (opcode == Op::EXPIRED) {
    // EXPIRED entry: db_id + key
    SET_OR_RETURN(ReadUInt(), db_id);
    SET_OR_RETURN(ReadString(), key);
  } else if (opcode == Op::COMMAND) {
    // COMMAND entry: db_id + txid + num_args + [args...]
    SET_OR_RETURN(ReadUInt(), db_id);
    SET_OR_RETURN(ReadUInt(), txid);
    SET_OR_RETURN(ReadUInt(), num_args);

    for (size_t i = 0; i < num_args; ++i) {
      SET_OR_RETURN(ReadString(), arg);
      args.push_back(std::move(arg));
    }
  }

  return ReadResult{opcode, db_id, txid, args};
}
```

## **Implementation Highlights**

### **Key Findings**

Through source code analysis, I identified these critical implementation points for df2redis:

1. **Multi-FLOW Architecture**: Must establish separate connections for each FLOW and handle them in parallel
2. **Global Barrier**: All FLOWs must complete RDB phase before sending STARTSTABLE to prevent data loss
3. **EOF Token Handling**: Must read and discard 40-byte EOF token after RDB data
4. **Journal Format**: Properly parse packed uint encoding and handle EXPIRED, COMMAND, PING, SELECT opcodes
5. **LSN Tracking**: Track executed journal record count for checkpoint/resume support

### **Critical Sections for df2redis**

When implementing df2redis, special attention is required for:

1. **FLOW Connection Management**: Establish and maintain multiple FLOW connections
2. **Barrier Synchronization**: Ensure all FLOWs complete RDB loading before proceeding
3. **Journal Parsing**: Correctly decode packed uint format and journal entry structure
4. **Command Replay**: Route commands to correct Redis Cluster node based on slot calculation
5. **Error Handling**: Properly handle connection failures and protocol errors

## **Summary**

Through in-depth analysis of Dragonfly source code, I documented the complete Replica replication protocol including:

1. 5-phase protocol flow (TCP Connection → Handshake → Full Sync → Barrier → Stable Sync)
2. Multi-FLOW architecture design and parallel replication mechanism
3. Global barrier synchronization to prevent data loss
4. Journal entry format and parsing logic
5. Command replay and LSN tracking

These findings are essential for correctly implementing df2redis' replication functionality, particularly in handling Dragonfly's unique multi-FLOW architecture and journal streaming protocol.
