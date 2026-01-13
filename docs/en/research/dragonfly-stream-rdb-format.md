# Dragonfly Stream RDB Format Detailed Analysis

## Research Context

When implementing Stream type data replication for df2redis, I needed to thoroughly understand the RDB serialization format for Streams in Dragonfly. By analyzing Dragonfly source code (`src/server/rdb_save.cc` and `src/server/rdb_load.cc`), I documented the complete save and load logic for Stream RDB format below.

## **1. Stream RDB Type Versions**

Dragonfly supports three versions of Stream RDB format:

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

- **`RDB_TYPE_STREAM_LISTPACKS`** (15): Original format
- **`RDB_TYPE_STREAM_LISTPACKS_2`** (19): Adds **`first_id`**, **`max_deleted_entry_id`**, **`entries_added`** fields
- **`RDB_TYPE_STREAM_LISTPACKS_3`** (21): Adds consumer **`active_time`** field

Version selection is controlled by **`FLAGS_stream_rdb_encode_v2`** flag:

`rdb_save.cc:206-208`

```cpp
    case OBJ_STREAM:
      return absl::GetFlag(FLAGS_stream_rdb_encode_v2) ? RDB_TYPE_STREAM_LISTPACKS_3
                                                       : RDB_TYPE_STREAM_LISTPACKS;
```

## **2. Stream RDB Save Order (Critical!)**

The complete save order in **`SaveStreamObject`** function:

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

**Exact Write Order:**

1. **Listpacks count** (**`raxSize`**)
2. **All Listpack nodes** (each contains key + listpack data)
3. **Stream length** (**`s->length`**)
4. **Last ID** (**`s->last_id.ms`** + **`s->last_id.seq`**)
5. **If V2/V3 version, additionally write:**
    - First ID (**`s->first_id.ms`** + **`s->first_id.seq`**)
    - Max deleted entry ID (**`s->max_deleted_entry_id.ms`** + **`s->max_deleted_entry_id.seq`**)
    - Entries added (**`s->entries_added`**)
6. **Consumer Groups count**
7. **For each Consumer Group:**
    - Group name
    - Last ID (ms + seq)
    - Entries read (V2+ only)
    - **Global PEL** (calls **`SaveStreamPEL`**)
    - **Consumers** (calls **`SaveStreamConsumers`**)

## **3. PEL (Pending Entry List) Save Format**

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

**PEL Format:**

1. PEL size (entry count)
2. For each entry:
    - Stream ID (16 bytes, big endian)
    - Delivery time (8 bytes, little endian)
    - Delivery count (encoded with **`SaveLen`**)

## **4. Consumer Save Format**

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

**Consumer Format:**

1. Consumers count
2. For each consumer:
    - Consumer name (string)
    - Seen time (8 bytes, little endian)
    - Active time (8 bytes, little endian, V3 only)
    - Consumer PEL (**Note: only saves IDs, not NACK data**)

## **5. Stream RDB Load Order**

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

**Read Order:**

1. Listpacks count
2. Read all listpack nodes (each contains stream_id + blob)
3. Stream length
4. Last ID (ms + seq)
5. If V2+: First ID, Max deleted entry ID, Entries added

**Consumer Groups Loading:**

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
      }
    }  // while (consumers_num)
  }    // while (cgroup_num)
```

Read order for each Consumer Group:

1. Group name
2. Last ID (ms + seq)
3. Entries read (V2+)
4. **Global PEL size + PEL entries**
5. **Consumers count**
6. For each consumer:
    - Consumer name
    - Seen time
    - Active time (V3+)
    - Consumer PEL size + PEL entry IDs

## **6. Key Data Structure Definitions**

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

## **Key Findings and Implementation Points**

### **⚠️ Critical Issue 1: Consumer PEL Format Difference**

Note the Consumer PEL save call:

`rdb_save.cc:757-763`

```cpp
    RETURN_ON_ERR(SaveStreamPEL(consumer->pel, false));
```

**Consumer PEL's `nacks` parameter is `false`**, meaning Consumer PEL only saves Stream ID (16 bytes), **NOT** delivery_time and delivery_count!

### **⚠️ Critical Issue 2: Loop Boundaries**

Each loop has explicit count prefix:

- Consumer Groups count is read upfront
- Global PEL size is obtained before reading PEL
- Consumers count is obtained before reading consumers
- Consumer PEL size is obtained before reading each consumer's PEL

### **⚠️ Critical Issue 3: Version Differences**

Ensure correct handling of three version differences:

- V1: No first_id, max_deleted_entry_id, entries_added
- V2: No consumer active_time
- V3: Full fields

### **⚠️ Critical Issue 4: Stream ID Encoding**

Stream IDs in PEL are stored in **16-byte big endian** format:

`rdb_save.cc:710-713`

```cpp
  while (raxNext(&ri)) {
    /* We store IDs in raw form as 128 big big endian numbers, like
     * they are inside the radix tree key. */
    RETURN_ON_ERR(WriteRaw(Bytes{ri.key, sizeof(streamID)}));
```

## **Implementation Recommendations**

**Most likely causes of parsing errors:**

1. **Consumer PEL parsing error**: When reading Consumer PEL, expecting to read delivery_time and delivery_count, but Consumer PEL actually only has Stream ID (16 bytes)! This causes read position misalignment and all subsequent data will parse incorrectly.
2. **Loop condition error**: Ensure you correctly read the length before reading each array/list, and loop counts are accurate. Pay special attention that Consumer Groups, Global PEL, Consumers, Consumer PEL all have their own length prefixes.
3. **Version check**: If RDB type version (15/19/21) is not correctly determined, you might try to read non-existent fields or skip fields that should be read.

When implementing, focus on the Consumer PEL parsing section to ensure it only reads 16 bytes of Stream ID, rather than attempting to read the complete NACK structure.

## **Summary**

Through in-depth analysis of Dragonfly source code, I clarified the exact save and load order of Stream RDB format, especially these key findings:

1. Consumer PEL only saves Stream ID, not NACK data
2. Each loop has explicit count prefix, must read in strict order
3. Three RDB versions have different field sets, need version-based handling
4. Stream IDs in PEL use 16-byte big endian encoding

These findings are critical for correctly implementing Stream type replication functionality in df2redis.
