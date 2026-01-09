# Dragonfly Stream RDB 格式详细分析

我已经找到了 Dragonfly 中 Stream 的完整 RDB 保存和加载逻辑。以下是关键信息：

## **1. Stream RDB 类型版本**

Dragonfly 支持三个版本的 Stream RDB 格式： 

`rdb.h:98-104`

```cpp
#define RDB_TYPE_STREAM_LISTPACKS 15
#define RDB_TYPE_HASH_LISTPACK 16
#define RDB_TYPE_ZSET_LISTPACK 17
#define RDB_TYPE_LIST_QUICKLIST_2   18
#define RDB_TYPE_STREAM_LISTPACKS_2 19
#define RDB_TYPE_SET_LISTPACK  20
#define RDB_TYPE_STREAM_LISTPACKS_3 21
```

- **`RDB_TYPE_STREAM_LISTPACKS`** (15): 原始格式
- **`RDB_TYPE_STREAM_LISTPACKS_2`** (19): 添加了 **`first_id`**, **`max_deleted_entry_id`**, **`entries_added`** 字段
- **`RDB_TYPE_STREAM_LISTPACKS_3`** (21): 添加了 consumer 的 **`active_time`** 字段

版本选择由 **`FLAGS_stream_rdb_encode_v2`** 标志控制： 

`rdb_save.cc:206-208`

```cpp
    case OBJ_STREAM:
      return absl::GetFlag(FLAGS_stream_rdb_encode_v2) ? RDB_TYPE_STREAM_LISTPACKS_3
                                                       : RDB_TYPE_STREAM_LISTPACKS;
```

## **2. Stream RDB 保存顺序（关键！）**

**`SaveStreamObject`** 函数的完整保存顺序如下： 

`rdb_save.cc:507-604`

```cpp
error_code RdbSerializer::SaveStreamObject(const PrimeValue& pv) {
  /* Store how many listpacks we have inside the radix tree. */
  stream* s = (stream*)pv.RObjPtr();
  rax* rax = s->rax_tree;

  const size_t rax_size = raxSize(rax);

  RETURN_ON_ERR(SaveLen(rax_size));

  /* Serialize all the listpacks inside the radix tree as they are,
   * when loading back, we'll use the first entry of each listpack
   * to insert it back into the radix tree. */
  raxIterator ri;
  raxStart(&ri, rax);
  raxSeek(&ri, "^", NULL, 0);

  auto stop_listpacks_rax = absl::MakeCleanup([&] { raxStop(&ri); });

  for (size_t i = 0; raxNext(&ri); i++) {
    uint8_t* lp = (uint8_t*)ri.data;
    size_t lp_bytes = lpBytes(lp);

    RETURN_ON_ERR(SaveString((uint8_t*)ri.key, ri.key_len));
    RETURN_ON_ERR(SaveString(lp, lp_bytes));

    FlushIfNeeded(FlushState::kFlushMidEntry);
  }

  std::move(stop_listpacks_rax).Invoke();

  /* Save the number of elements inside the stream. We cannot obtain
   * this easily later, since our macro nodes should be checked for
   * number of items: not a great CPU / space tradeoff. */

  RETURN_ON_ERR(SaveLen(s->length));

  /* Save the last entry ID. */
  RETURN_ON_ERR(SaveLen(s->last_id.ms));
  RETURN_ON_ERR(SaveLen(s->last_id.seq));

  uint8_t rdb_type = RdbObjectType(pv);

  // 'first_id', 'max_deleted_entry_id' and 'entries_added' are added
  // in RDB_TYPE_STREAM_LISTPACKS_2
  if (rdb_type >= RDB_TYPE_STREAM_LISTPACKS_2) {
    /* Save the first entry ID. */
    RETURN_ON_ERR(SaveLen(s->first_id.ms));
    RETURN_ON_ERR(SaveLen(s->first_id.seq));

    /* Save the maximal tombstone ID. */
    RETURN_ON_ERR(SaveLen(s->max_deleted_entry_id.ms));
    RETURN_ON_ERR(SaveLen(s->max_deleted_entry_id.seq));

    /* Save the offset. */
    RETURN_ON_ERR(SaveLen(s->entries_added));
  }
  /* The consumer groups and their clients are part of the stream
   * type, so serialize every consumer group. */

  /* Save the number of groups. */
  size_t num_cgroups = s->cgroups ? raxSize(s->cgroups) : 0;
  RETURN_ON_ERR(SaveLen(num_cgroups));

  if (num_cgroups) {
    /* Serialize each consumer group. */
    raxStart(&ri, s->cgroups);
    raxSeek(&ri, "^", NULL, 0);

    auto stop_cgroups_rax = absl::MakeCleanup([&] { raxStop(&ri); });

    while (raxNext(&ri)) {
      streamCG* cg = (streamCG*)ri.data;

      /* Save the group name. */
      RETURN_ON_ERR(SaveString((uint8_t*)ri.key, ri.key_len));

      /* Last ID. */
      RETURN_ON_ERR(SaveLen(cg->last_id.ms));

      RETURN_ON_ERR(SaveLen(cg->last_id.seq));

      if (rdb_type >= RDB_TYPE_STREAM_LISTPACKS_2) {
        /* Save the group's logical reads counter. */
        RETURN_ON_ERR(SaveLen(cg->entries_read));
      }

      /* Save the global PEL. */
      RETURN_ON_ERR(SaveStreamPEL(cg->pel, true));

      /* Save the consumers of this group. */
      RETURN_ON_ERR(SaveStreamConsumers(rdb_type >= RDB_TYPE_STREAM_LISTPACKS_3, cg));
    }
  }

  FlushIfNeeded(FlushState::kFlushEndEntry);

  return error_code{};
}
```

**精确的写入顺序：**

1. **Listpacks 数量** (**`raxSize`**)
2. **所有 Listpack 节点**（每个节点包含 key + listpack 数据）
3. **Stream 长度** (**`s->length`**)
4. **Last ID** (**`s->last_id.ms`** + **`s->last_id.seq`**)
5. **如果是 V2/V3 版本，额外写入：**
    - First ID (**`s->first_id.ms`** + **`s->first_id.seq`**)
    - Max deleted entry ID (**`s->max_deleted_entry_id.ms`** + **`s->max_deleted_entry_id.seq`**)
    - Entries added (**`s->entries_added`**)
6. **Consumer Groups 数量**
7. **每个 Consumer Group：**
    - Group 名称
    - Last ID (ms + seq)
    - Entries read (仅 V2+)
    - **Global PEL**（调用 **`SaveStreamPEL`**）
    - **Consumers**（调用 **`SaveStreamConsumers`**）

## **3. PEL (Pending Entry List) 保存格式**

**`rdb_save.cc:698-728`**

```cpp
error_code RdbSerializer::SaveStreamPEL(rax* pel, bool nacks) {
  /* Number of entries in the PEL. */

  RETURN_ON_ERR(SaveLen(raxSize(pel)));

  /* Save each entry. */
  raxIterator ri;
  raxStart(&ri, pel);
  raxSeek(&ri, "^", NULL, 0);
  auto cleanup = absl::MakeCleanup([&] { raxStop(&ri); });

  while (raxNext(&ri)) {
    /* We store IDs in raw form as 128 big big endian numbers, like
     * they are inside the radix tree key. */
    RETURN_ON_ERR(WriteRaw(Bytes{ri.key, sizeof(streamID)}));

    if (nacks) {
      streamNACK* nack = (streamNACK*)ri.data;
      uint8_t buf[8];
      absl::little_endian::Store64(buf, nack->delivery_time);
      RETURN_ON_ERR(WriteRaw(buf));
      RETURN_ON_ERR(SaveLen(nack->delivery_count));

      /* We don't save the consumer name: we'll save the pending IDs
       * for each consumer in the consumer PEL, and resolve the consumer
       * at loading time. */
    }
  }

  return error_code{};
}
```

**PEL 格式：**

1. PEL 大小（entry 数量）
2. 对于每个 entry：
    - Stream ID（16 字节，big endian）
    - Delivery time（8 字节，little endian）
    - Delivery count（使用 **`SaveLen`** 编码）

## **4. Consumer 保存格式**

**`rdb_save.cc:730-766`**

```cpp
error_code RdbSerializer::SaveStreamConsumers(bool save_active, streamCG* cg) {
  /* Number of consumers in this consumer group. */

  RETURN_ON_ERR(SaveLen(raxSize(cg->consumers)));

  /* Save each consumer. */
  raxIterator ri;
  raxStart(&ri, cg->consumers);
  raxSeek(&ri, "^", NULL, 0);
  auto cleanup = absl::MakeCleanup([&] { raxStop(&ri); });
  uint8_t buf[8];

  while (raxNext(&ri)) {
    streamConsumer* consumer = (streamConsumer*)ri.data;

    /* Consumer name. */
    RETURN_ON_ERR(SaveString(ri.key, ri.key_len));

    /* seen time. */
    absl::little_endian::Store64(buf, consumer->seen_time);
    RETURN_ON_ERR(WriteRaw(buf));

    if (save_active) {
      /* Active time. */
      absl::little_endian::Store64(buf, consumer->active_time);
      RETURN_ON_ERR(WriteRaw(buf));
    }
    /* Consumer PEL, without the ACKs (see last parameter of the function
     * passed with value of 0), at loading time we'll lookup the ID
     * in the consumer group global PEL and will put a reference in the
     * consumer local PEL. */

    RETURN_ON_ERR(SaveStreamPEL(consumer->pel, false));
  }

  return error_code{};
}
```

**Consumer 格式：**

1. Consumers 数量
2. 对于每个 consumer：
    - Consumer 名称（字符串）
    - Seen time（8 字节，little endian）
    - Active time（8 字节，little endian，仅 V3）
    - Consumer PEL（**注意：只保存 ID，不保存 NACK 数据**）

## **5. Stream RDB 加载顺序**

**`rdb_load.cc:1637-1698`**

```cpp
auto RdbLoaderBase::ReadStreams(int rdbtype) -> io::Result<OpaqueObj> {
  size_t listpacks;
  if (pending_read_.remaining > 0) {
    listpacks = pending_read_.remaining;
  } else {
    SET_OR_UNEXPECT(LoadLen(NULL), listpacks);
  }

  unique_ptr<LoadTrace> load_trace(new LoadTrace);
  // Streams pack multiple entries into each stream node (4Kb or 100
  // entries), therefore using a smaller segment length than kMaxBlobLen.
  size_t n = std::min<size_t>(listpacks, 512);
  load_trace->arr.resize(n * 2);

  error_code ec;
  for (size_t i = 0; i < n; ++i) {
    /* Get the master ID, the one we'll use as key of the radix tree
     * node: the entries inside the listpack itself are delta-encoded
     * relatively to this ID. */
    RdbVariant stream_id, blob;
    ec = ReadStringObj(&stream_id);
    if (ec)
      return make_unexpected(ec);
    if (StrLen(stream_id) != sizeof(streamID)) {
      LOG(ERROR) << "Stream node key entry is not the size of a stream ID";

      return Unexpected(errc::rdb_file_corrupted);
    }

    ec = ReadStringObj(&blob);
    if (ec)
      return make_unexpected(ec);
    if (StrLen(blob) == 0) {
      LOG(ERROR) << "Stream listpacks loading failed";
      return Unexpected(errc::rdb_file_corrupted);
    }

    load_trace->arr[2 * i].rdb_var = std::move(stream_id);
    load_trace->arr[2 * i + 1].rdb_var = std::move(blob);
  }

  // If there are still unread elements, cache the number of remaining
  // elements, or clear if the full object has been read.
  //
  // We only load the stream metadata and consumer groups in the final read,
  // so if there are still unread elements return the partial stream.
  if (listpacks > n) {
    pending_read_.remaining = listpacks - n;
    return OpaqueObj{std::move(load_trace), rdbtype};
  }

  pending_read_.remaining = 0;

  // Load stream metadata.
  load_trace->stream_trace.reset(new StreamTrace);

  /* Load total number of items inside the stream. */
  SET_OR_UNEXPECT(LoadLen(nullptr), load_trace->stream_trace->stream_len);

  /* Load the last entry ID. */
  SET_OR_UNEXPECT(LoadLen(nullptr), load_trace->stream_trace->last_id.ms);
  SET_OR_UNEXPECT(LoadLen(nullptr), load_trace->stream_trace->last_id.seq);
```

**读取顺序：**

1. Listpacks 数量
2. 读取所有 listpack 节点（每个包含 stream_id + blob）
3. Stream length
4. Last ID (ms + seq)
5. 如果是 V2+：First ID, Max deleted entry ID, Entries added

**Consumer Groups 加载：** 

`rdb_load.cc:1718-1817`

```cpp
  /* Consumer groups loading */
  uint64_t cgroups_count;
  SET_OR_UNEXPECT(LoadLen(nullptr), cgroups_count);
  load_trace->stream_trace->cgroup.resize(cgroups_count);

  for (size_t i = 0; i < cgroups_count; ++i) {
    auto& cgroup = load_trace->stream_trace->cgroup[i];
    /* Get the consumer group name and ID. We can then create the
     * consumer group ASAP and populate its structure as
     * we read more data. */

    // sds cgname;
    RdbVariant cgname;
    ec = ReadStringObj(&cgname);
    if (ec)
      return make_unexpected(ec);
    cgroup.name = std::move(cgname);

    SET_OR_UNEXPECT(LoadLen(nullptr), cgroup.ms);
    SET_OR_UNEXPECT(LoadLen(nullptr), cgroup.seq);

    cgroup.entries_read = 0;
    if (rdbtype >= RDB_TYPE_STREAM_LISTPACKS_2) {
      SET_OR_UNEXPECT(LoadLen(nullptr), cgroup.entries_read);
    }

    /* Load the global PEL for this consumer group, however we'll
     * not yet populate the NACK structures with the message
     * owner, since consumers for this group and their messages will
     * be read as a next step. So for now leave them not resolved
     * and later populate it. */
    uint64_t pel_size;
    SET_OR_UNEXPECT(LoadLen(nullptr), pel_size);

    cgroup.pel_arr.resize(pel_size);

    for (size_t j = 0; j < pel_size; ++j) {
      auto& pel = cgroup.pel_arr[j];
      error_code ec = FetchBuf(pel.rawid.size(), pel.rawid.data());
      if (ec) {
        LOG(ERROR) << "Stream PEL ID loading failed.";
        return make_unexpected(ec);
      }

      SET_OR_UNEXPECT(FetchInt<int64_t>(), pel.delivery_time);
      SET_OR_UNEXPECT(LoadLen(nullptr), pel.delivery_count);
    }

    /* Now that we loaded our global PEL, we need to load the
     * consumers and their local PELs. */
    uint64_t consumers_num;
    SET_OR_UNEXPECT(LoadLen(nullptr), consumers_num);
    cgroup.cons_arr.resize(consumers_num);

    for (size_t j = 0; j < consumers_num; ++j) {
      auto& consumer = cgroup.cons_arr[j];
      ec = ReadStringObj(&consumer.name);
      if (ec)
        return make_unexpected(ec);

      SET_OR_UNEXPECT(FetchInt<int64_t>(), consumer.seen_time);

      if (rdbtype >= RDB_TYPE_STREAM_LISTPACKS_3) {
        SET_OR_UNEXPECT(FetchInt<int64_t>(), consumer.active_time);
      } else {
        /* That's the best estimate we got */
        consumer.active_time = consumer.seen_time;
      }

      /* Load the PEL about entries owned by this specific
       * consumer. */
      SET_OR_UNEXPECT(LoadLen(nullptr), pel_size);
      consumer.nack_arr.resize(pel_size);
      for (size_t k = 0; k < pel_size; ++k) {
        auto& nack = consumer.nack_arr[k];
        // unsigned char rawid[sizeof(streamID)];
        error_code ec = FetchBuf(nack.size(), nack.data());
        if (ec) {
          LOG(ERROR) << "Stream PEL ID loading failed.";
          return make_unexpected(ec);
        }
        /*streamNACK* nack = (streamNACK*)raxFind(cgroup->pel, rawid, sizeof(rawid));
        if (nack == raxNotFound) {
          LOG(ERROR) << "Consumer entry not found in group global PEL";
          return Unexpected(errc::rdb_file_corrupted);
        }*/

        /* Set the NACK consumer, that was left to NULL when
         * loading the global PEL. Then set the same shared
         * NACK structure also in the consumer-specific PEL. */
        /*
        nack->consumer = consumer;
        if (!raxTryInsert(consumer->pel, rawid, sizeof(rawid), nack, NULL)) {
          LOG(ERROR) << "Duplicated consumer PEL entry loading a stream consumer group";
          streamFreeNACK(nack);
          return Unexpected(errc::duplicate_key);
        }*/
      }
    }  // while (consumers_num)
  }    // while (cgroup_num)
```

每个 Consumer Group 的读取顺序：

1. Group 名称
2. Last ID (ms + seq)
3. Entries read (V2+)
4. **Global PEL size + PEL entries**
5. **Consumers 数量**
6. 对于每个 consumer：
    - Consumer 名称
    - Seen time
    - Active time (V3+)
    - Consumer PEL size + PEL entry IDs

## **6. 关键数据结构定义**

**`rdb_load.h:74-109`** 

```cpp
  struct StreamPelTrace {
    std::array<uint8_t, 16> rawid;
    int64_t delivery_time;
    uint64_t delivery_count;
  };

  struct StreamConsumerTrace {
    RdbVariant name;
    int64_t seen_time;
    int64_t active_time;
    std::vector<std::array<uint8_t, 16>> nack_arr;
  };

  struct StreamID {
    uint64_t ms = 0;
    uint64_t seq = 0;
  };

  struct StreamCGTrace {
    RdbVariant name;
    uint64_t ms;
    uint64_t seq;
    uint64_t entries_read;
    std::vector<StreamPelTrace> pel_arr;
    std::vector<StreamConsumerTrace> cons_arr;
  };

  struct StreamTrace {
    size_t lp_len;
    size_t stream_len;
    StreamID last_id;
    StreamID first_id;             /* The first non-tombstone entry, zero if empty. */
    StreamID max_deleted_entry_id; /* The maximal ID that was deleted. */
    uint64_t entries_added = 0;    /* All time count of elements added. */
    std::vector<StreamCGTrace> cgroup;
  };
```

**`stream.h:16-29`** 

```cpp
typedef struct streamID {
    uint64_t ms;        /* Unix time in milliseconds. */
    uint64_t seq;       /* Sequence number. */
} streamID;

typedef struct stream {
    rax *rax_tree;               /* The radix tree holding the stream. */
    uint64_t length;        /* Current number of elements inside this stream. */
    streamID last_id;       /* Zero if there are yet no items. */
    streamID first_id;      /* The first non-tombstone entry, zero if empty. */
    streamID max_deleted_entry_id;  /* The maximal ID that was deleted. */
    uint64_t entries_added; /* All time count of elements added. */
    rax *cgroups;           /* Consumer groups dictionary: name -> streamCG */
} stream;
```

**`stream.h:59-102`**

```cpp
/* Consumer group. */
typedef struct streamCG {
    streamID last_id;       /* Last delivered (not acknowledged) ID for this
                               group. Consumers that will just ask for more
                               messages will served with IDs > than this. */
    long long entries_read; /* In a perfect world (CG starts at 0-0, no dels, no
                               XGROUP SETID, ...), this is the total number of
                               group reads. In the real world, the reasoning behind
                               this value is detailed at the top comment of
                               streamEstimateDistanceFromFirstEverEntry(). */
    rax *pel;               /* Pending entries list. This is a radix tree that
                               has every message delivered to consumers (without
                               the NOACK option) that was yet not acknowledged
                               as processed. The key of the radix tree is the
                               ID as a 64 bit big endian number, while the
                               associated value is a streamNACK structure.*/
    rax *consumers;         /* A radix tree representing the consumers by name
                               and their associated representation in the form
                               of streamConsumer structures. */
} streamCG;

/* A specific consumer in a consumer group.  */
typedef struct streamConsumer {
    mstime_t seen_time;         /* Last time this consumer tried to perform an action (attempted reading/claiming). */
    mstime_t active_time;       /* Last time this consumer was active (successful reading/claiming). */
    sds name;                   /* Consumer name. This is how the consumer
                                   will be identified in the consumer group
                                   protocol. Case sensitive. */
    rax *pel;                   /* Consumer specific pending entries list: all
                                   the pending messages delivered to this
                                   consumer not yet acknowledged. Keys are
                                   big endian message IDs, while values are
                                   the same streamNACK structure referenced
                                   in the "pel" of the consumer group structure
                                   itself, so the value is shared. */
} streamConsumer;

/* Pending (yet not acknowledged) message in a consumer group. */
typedef struct streamNACK {
    mstime_t delivery_time;     /* Last time this message was delivered. */
    uint64_t delivery_count;    /* Number of times this message was delivered.*/
    streamConsumer *consumer;   /* The consumer this message was delivered to
                                   in the last delivery. */
} streamNACK;
```

## **7. 可能导致你的 bug 的关键点**

根据你的描述，以下是最可能的问题点：

### **⚠️ 关键问题 1：Consumer PEL 格式差异**

注意 Consumer PEL 的保存调用： 

`rdb_save.cc:757-763`

```cpp
error_code RdbSerializer::SaveStreamConsumers(bool save_active, streamCG* cg) {
  /* Number of consumers in this consumer group. */

  RETURN_ON_ERR(SaveLen(raxSize(cg->consumers)));

  /* Save each consumer. */
  raxIterator ri;
  raxStart(&ri, cg->consumers);
  raxSeek(&ri, "^", NULL, 0);
  auto cleanup = absl::MakeCleanup([&] { raxStop(&ri); });
  uint8_t buf[8];

  while (raxNext(&ri)) {
    streamConsumer* consumer = (streamConsumer*)ri.data;

    /* Consumer name. */
    RETURN_ON_ERR(SaveString(ri.key, ri.key_len));

    /* seen time. */
    absl::little_endian::Store64(buf, consumer->seen_time);
    RETURN_ON_ERR(WriteRaw(buf));

    if (save_active) {
      /* Active time. */
      absl::little_endian::Store64(buf, consumer->active_time);
      RETURN_ON_ERR(WriteRaw(buf));
    }
    /* Consumer PEL, without the ACKs (see last parameter of the function
     * passed with value of 0), at loading time we'll lookup the ID
     * in the consumer group global PEL and will put a reference in the
     * consumer local PEL. */

    RETURN_ON_ERR(SaveStreamPEL(consumer->pel, false));
  }

  return error_code{};
}
```

**Consumer PEL 的 `nacks` 参数是 `false`**，这意味着 Consumer PEL 只保存 Stream ID（16 字节），**不保存** delivery_time 和 delivery_count！

### **⚠️ 关键问题 2：循环边界**

每个循环都有明确的数量前缀：

- Consumer Groups 数量在前面读取
- Global PEL size 在读取 PEL 前获取
- Consumers 数量在读取 consumers 前获取
- Consumer PEL size 在读取每个 consumer 的 PEL 前获取

### **⚠️ 关键问题 3：版本差异**

确保你的代码正确处理三个版本的差异：

- V1: 没有 first_id, max_deleted_entry_id, entries_added
- V2: 没有 consumer active_time
- V3: 完整字段

### **⚠️ 关键问题 4：Stream ID 编码**

Stream ID 在 PEL 中是以 **16 字节 big endian** 格式存储的： 

`rdb_save.cc:710-713`

```cpp
  while (raxNext(&ri)) {
    /* We store IDs in raw form as 128 big big endian numbers, like
     * they are inside the radix tree key. */
    RETURN_ON_ERR(WriteRaw(Bytes{ri.key, sizeof(streamID)}));
```

## **Notes**

**最可能导致卡死的原因：**

1. **Consumer PEL 解析错误**：你可能在读取 Consumer PEL 时，期望读取 delivery_time 和 delivery_count，但实际上 Consumer PEL 只有 Stream ID（16 字节）！这会导致读取位置错位，后续所有数据都会解析错误。
2. **循环条件错误**：确保你在读取每个数组/列表前都正确读取了其长度，并且循环次数准确。特别注意 Consumer Groups、Global PEL、Consumers、Consumer PEL 都有各自的长度前缀。
3. **版本判断**：如果你没有正确判断 RDB 类型版本（15/19/21），可能会尝试读取不存在的字段或跳过应该读取的字段。

建议检查你的代码中 Consumer PEL 的解析部分，确保它只读取 16 字节的 Stream ID，而不是尝试读取完整的 NACK 结构。