package com.netease.nim.camellia.redis.proxy.upstream.kv.command.hash;

import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.enums.RedisCommand;
import com.netease.nim.camellia.redis.proxy.monitor.KvCacheMonitor;
import com.netease.nim.camellia.redis.proxy.reply.*;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.NoOpResult;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.Result;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.WriteBufferValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.RedisHash;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.HashLRUCache;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.CommanderConfig;
import com.netease.nim.camellia.redis.proxy.upstream.kv.kv.KeyValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.EncodeVersion;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyMeta;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyType;
import com.netease.nim.camellia.redis.proxy.upstream.kv.utils.BytesUtils;
import com.netease.nim.camellia.redis.proxy.util.Utils;
import com.netease.nim.camellia.tools.utils.BytesKey;

import java.util.*;

/**
 *
 * HSET key field value [field value ...]
 * <p>
 * Created by caojiajun on 2024/4/7
 */
public class HSetCommander extends Hash0Commander {


    public HSetCommander(CommanderConfig commanderConfig) {
        super(commanderConfig);
    }

    @Override
    public RedisCommand redisCommand() {
        return RedisCommand.HSET;
    }

    @Override
    protected boolean parse(Command command) {
        byte[][] objects = command.getObjects();
        if (objects.length < 4) {
            return false;
        }
        return objects.length % 2 == 0;
    }

    @Override
    protected Reply execute(int slot, Command command) {
        byte[][] objects = command.getObjects();
        byte[] key = objects[1];

        Map<BytesKey, byte[]> fieldMap = new HashMap<>();
        for (int i=2; i<objects.length; i+=2) {
            byte[] field = objects[i];
            byte[] value = objects[i + 1];
            fieldMap.put(new BytesKey(field), value);
        }
        int fieldSize = fieldMap.size();

        boolean first = false;

        //check meta
        KeyMeta keyMeta = keyMetaServer.getKeyMeta(slot, key);
        if (keyMeta == null) {
            EncodeVersion encodeVersion = keyDesign.hashEncodeVersion();
            if (encodeVersion == EncodeVersion.version_0) {
                int count = fieldMap.size();
                byte[] extra = BytesUtils.toBytes(count);
                keyMeta = new KeyMeta(encodeVersion, KeyType.hash, System.currentTimeMillis(), -1, extra);
            } else if (encodeVersion == EncodeVersion.version_1) {
                keyMeta = new KeyMeta(encodeVersion, KeyType.hash, System.currentTimeMillis(), -1);
            } else {
                return ErrorReply.INTERNAL_ERROR;
            }
            keyMetaServer.createOrUpdateKeyMeta(slot, key, keyMeta);
            first = true;
        } else {
            if (keyMeta.getKeyType() != KeyType.hash) {
                return ErrorReply.WRONG_TYPE;
            }
        }

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        KvCacheMonitor.Type type = null;

        int existsCount = -1;
        Map<BytesKey, byte[]> existsMap = null;

        Result result = null;
        if (first) {
            existsCount = 0;
            result = hashWriteBuffer.put(cacheKey, new RedisHash(new HashMap<>(fieldMap)));
            //
            if (result != NoOpResult.INSTANCE) {
                KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
                type = KvCacheMonitor.Type.write_buffer;
            }
        } else {
            WriteBufferValue<RedisHash> value = hashWriteBuffer.get(cacheKey);
            if (value != null) {
                RedisHash hash = value.getValue();
                existsMap = hash.hset(fieldMap);
                existsCount = existsMap.size();
                result = hashWriteBuffer.put(cacheKey, hash);
                KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
                type = KvCacheMonitor.Type.write_buffer;
            }
        }

        if (cacheConfig.isHashLocalCacheEnable()) {
            HashLRUCache hashLRUCache = cacheConfig.getHashLRUCache();

            Map<BytesKey, byte[]> existMapByCache = null;
            if (first) {
                hashLRUCache.putAllForWrite(slot, cacheKey, new RedisHash(new HashMap<>(fieldMap)));
            } else {
                existMapByCache = hashLRUCache.hset(slot, cacheKey, fieldMap);
                if (existMapByCache == null) {
                    boolean hotKey = hashLRUCache.isHotKey(key, redisCommand());
                    if (hotKey) {
                        //
                        type = KvCacheMonitor.Type.kv_store;
                        KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
                        //
                        RedisHash redisHash = loadLRUCache(slot, keyMeta, key);
                        hashLRUCache.putAllForWrite(slot, cacheKey, redisHash);
                        existMapByCache = redisHash.hset(fieldMap);
                    }
                } else {
                    KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                    type = KvCacheMonitor.Type.local_cache;
                }
            }

            if (existsMap == null && existsCount < 0 && existMapByCache != null) {
                existsCount = existMapByCache.size();
                existsMap = existMapByCache;
            }

            if (result == null) {
                RedisHash hash = hashLRUCache.getForWrite(slot, cacheKey);
                if (hash != null) {
                    result = hashWriteBuffer.put(cacheKey, hash.duplicate());
                }
            }
        }

        if (result == null) {
            result = NoOpResult.INSTANCE;
        }

        if (!first && existsMap != null && !existsMap.isEmpty()) {
            for (Map.Entry<BytesKey, byte[]> entry : existsMap.entrySet()) {
                byte[] value = fieldMap.get(entry.getKey());
                if (Arrays.equals(value, entry.getValue())) {
                    fieldMap.remove(entry.getKey());
                }
            }
        }

        EncodeVersion encodeVersion = keyMeta.getEncodeVersion();

        if (first || encodeVersion == EncodeVersion.version_1) {
            if (type == null) {
                KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
            }
            List<KeyValue> list = new ArrayList<>();
            for (Map.Entry<BytesKey, byte[]> entry : fieldMap.entrySet()) {
                byte[] field = entry.getKey().getKey();
                byte[] value = entry.getValue();
                byte[] subKey = keyDesign.hashFieldSubKey(keyMeta, key, field);
                KeyValue keyValue = new KeyValue(subKey, value);
                list.add(keyValue);
            }
            batchPut(slot, cacheKey, result, list);
            return IntegerReply.parse(fieldSize);
        }

        if (encodeVersion == EncodeVersion.version_0) {
            byte[][] subKeys = new byte[fieldMap.size()][];
            List<KeyValue> list = new ArrayList<>();
            int i=0;
            for (Map.Entry<BytesKey, byte[]> entry : fieldMap.entrySet()) {
                byte[] field = entry.getKey().getKey();
                byte[] value = entry.getValue();
                byte[] subKey = keyDesign.hashFieldSubKey(keyMeta, key, field);
                KeyValue keyValue = new KeyValue(subKey, value);
                list.add(keyValue);
                subKeys[i] = subKey;
                i++;
            }
            if (existsCount < 0) {
                KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
                boolean[] exists = kvClient.exists(slot, subKeys);
                existsCount = Utils.count(exists);
            }
            int add = fieldSize - existsCount;
            if (add > 0) {
                int size = BytesUtils.toInt(keyMeta.getExtra());
                size = size + add;
                keyMeta = new KeyMeta(keyMeta.getEncodeVersion(), keyMeta.getKeyType(),
                        keyMeta.getKeyVersion(), keyMeta.getExpireTime(), BytesUtils.toBytes(size));
                keyMetaServer.createOrUpdateKeyMeta(slot, key, keyMeta);
            }
            batchPut(slot, cacheKey, result, list);
            return IntegerReply.parse(add);
        }

        return ErrorReply.INTERNAL_ERROR;
    }

    private void batchPut(int slot, byte[] cacheKey, Result result, List<KeyValue> list) {
        if (!result.isKvWriteDelayEnable()) {
            kvClient.batchPut(slot, list);
        } else {
            submitAsyncWriteTask(slot, result, () -> kvClient.batchPut(slot, list));
        }
    }

}
