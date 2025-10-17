package com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset;

import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.enums.RedisCommand;
import com.netease.nim.camellia.redis.proxy.monitor.KvCacheMonitor;
import com.netease.nim.camellia.redis.proxy.reply.ErrorReply;
import com.netease.nim.camellia.redis.proxy.reply.MultiBulkReply;
import com.netease.nim.camellia.redis.proxy.reply.Reply;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.WriteBufferValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.RedisZSet;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ValueWrapper;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ZSetLRUCache;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.CommanderConfig;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.*;
import com.netease.nim.camellia.redis.proxy.upstream.kv.kv.KeyValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.kv.Sort;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.EncodeVersion;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyMeta;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyType;
import com.netease.nim.camellia.redis.proxy.upstream.kv.utils.BytesUtils;
import com.netease.nim.camellia.redis.proxy.util.ErrorLogCollector;
import com.netease.nim.camellia.redis.proxy.util.Utils;
import com.netease.nim.camellia.tools.utils.BytesKey;

import java.util.ArrayList;
import java.util.List;

/**
 * ZREVRANGEBYLEX key max min [LIMIT offset count]
 * <p>
 * Created by caojiajun on 2024/4/11
 */
public class ZRevRangeByLexCommander extends ZSet0Commander {

    public ZRevRangeByLexCommander(CommanderConfig commanderConfig) {
        super(commanderConfig);
    }

    @Override
    public RedisCommand redisCommand() {
        return RedisCommand.ZREVRANGEBYLEX;
    }

    @Override
    protected boolean parse(Command command) {
        byte[][] objects = command.getObjects();
        return objects.length >= 4;
    }

    @Override
    public Reply runToCompletion(int slot, Command command) {
        byte[][] objects = command.getObjects();
        byte[] key = objects[1];
        ValueWrapper<KeyMeta> valueWrapper = keyMetaServer.runToCompletion(slot, key);
        if (valueWrapper == null) {
            return null;
        }
        KeyMeta keyMeta = valueWrapper.get();
        if (keyMeta == null) {
            return MultiBulkReply.EMPTY;
        }
        if (keyMeta.getKeyType() != KeyType.zset) {
            return ErrorReply.WRONG_TYPE;
        }

        ZSetLex minLex;
        ZSetLex maxLex;
        ZSetLimit limit;
        try {
            minLex = ZSetLex.fromLex(objects[3]);
            maxLex = ZSetLex.fromLex(objects[2]);
            if (minLex == null || maxLex == null) {
                return new ErrorReply("ERR min or max not valid string range item");
            }
            limit = ZSetLimit.fromBytes(objects, 4);
        } catch (Exception e) {
            ErrorLogCollector.collect(ZRevRangeByLexCommander.class, "zrevrangebylex command syntax error, illegal min/max/limit");
            return ErrorReply.SYNTAX_ERROR;
        }
        if (minLex.isMax() || maxLex.isMin()) {
            return MultiBulkReply.EMPTY;
        }

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return ZSetTupleUtils.toReply(list, false);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);

            if (zSet != null) {
                List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return ZSetTupleUtils.toReply(list, false);
            }
        }
        return null;
    }

    @Override
    protected Reply execute(int slot, Command command) {
        byte[][] objects = command.getObjects();
        byte[] key = objects[1];
        KeyMeta keyMeta = keyMetaServer.getKeyMeta(slot, key);
        if (keyMeta == null) {
            return MultiBulkReply.EMPTY;
        }
        if (keyMeta.getKeyType() != KeyType.zset) {
            return ErrorReply.WRONG_TYPE;
        }

        EncodeVersion encodeVersion = keyMeta.getEncodeVersion();

        ZSetLex minLex;
        ZSetLex maxLex;
        ZSetLimit limit;
        try {
            minLex = ZSetLex.fromLex(objects[3]);
            maxLex = ZSetLex.fromLex(objects[2]);
            if (minLex == null || maxLex == null) {
                return new ErrorReply("ERR min or max not valid string range item");
            }
            limit = ZSetLimit.fromBytes(objects, 4);
        } catch (Exception e) {
            ErrorLogCollector.collect(ZRevRangeByLexCommander.class, "zrevrangebylex command syntax error, illegal min/max/limit");
            return ErrorReply.SYNTAX_ERROR;
        }
        if (minLex.isMax() || maxLex.isMin()) {
            return MultiBulkReply.EMPTY;
        }

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return ZSetTupleUtils.toReply(list, false);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);

            if (zSet != null) {
                List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return ZSetTupleUtils.toReply(list, false);
            }

            boolean hotKey = zSetLRUCache.isHotKey(key, redisCommand());

            if (hotKey) {
                zSet = loadLRUCache(slot, keyMeta, key);
                if (zSet != null) {
                    //
                    zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
                    //
                    List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);

                    KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());

                    return ZSetTupleUtils.toReply(list, false);
                }
            }
        }

        if (encodeVersion == EncodeVersion.version_0) {
            KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
            if (!kvClient.supportReverseScan()) {
                return zrevrangeByLexVersion0NotSupportReverseScan(slot, keyMeta, key, cacheKey, minLex, maxLex, limit);
            }
            List<ZSetTuple> list = zrevrangeByLexVersion0(slot, keyMeta, key, minLex, maxLex, limit);
            return ZSetTupleUtils.toReply(list, false);
        }

        if (encodeVersion == EncodeVersion.version_1) {
            RedisZSet zSet = loadLRUCache(slot, keyMeta, key);
            if (zSet != null) {
                if (cacheConfig.isZSetLocalCacheEnable()) {
                    ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();
                    //
                    zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
                    //
                }

                List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);

                KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());

                return ZSetTupleUtils.toReply(list, false);
            }
        }

        return ErrorReply.INTERNAL_ERROR;
    }

    private Reply zrevrangeByLexVersion0NotSupportReverseScan(int slot, KeyMeta keyMeta, byte[] key, byte[] cacheKey, ZSetLex minLex, ZSetLex maxLex, ZSetLimit limit) {
        RedisZSet zSet = loadLRUCache(slot, keyMeta, key);
        if (zSet == null) {
            return Utils.commandNotSupport(RedisCommand.ZREVRANGEBYLEX);
        }
        //
        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();
            zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
        }
        //
        List<ZSetTuple> list = zSet.zrevrangeByLex(minLex, maxLex, limit);

        return ZSetTupleUtils.toReply(list, false);
    }

    private List<ZSetTuple> zrevrangeByLexVersion0(int slot, KeyMeta keyMeta, byte[] key, ZSetLex minLex, ZSetLex maxLex, ZSetLimit limit) {
        byte[] startKey;
        if (maxLex.isMax()) {
            startKey = BytesUtils.nextBytes(keyDesign.zsetMemberSubKey1(keyMeta, key, new byte[0]));
        } else {
            startKey = keyDesign.zsetMemberSubKey1(keyMeta, key, maxLex.getLex());
        }
        byte[] endKey;
        if (minLex.isMin()) {
            endKey = keyDesign.zsetMemberSubKey1(keyMeta, key, new byte[0]);
        } else {
            if (minLex.isExcludeLex()) {
                endKey = keyDesign.zsetMemberSubKey1(keyMeta, key, minLex.getLex());
            } else {
                endKey = BytesUtils.lastBytes(keyDesign.zsetMemberSubKey1(keyMeta, key, minLex.getLex()));
            }
        }
        byte[] prefix = keyDesign.subKeyPrefix(keyMeta, key);
        //
        List<ZSetTuple> result = new ArrayList<>(limit.getCount() < 0 ? 16 : Math.min(limit.getCount(), 100));
        int batch = kvConfig.scanBatch();
        int count = 0;
        int loop = 0;
        boolean includeStartKey;
        while (true) {
            if (limit.getCount() > 0) {
                batch = Math.min(kvConfig.scanBatch(), limit.getCount() - result.size());
            }
            if (loop == 0) {
                includeStartKey = !maxLex.isExcludeLex();
            } else {
                includeStartKey = false;
            }
            List<KeyValue> scan = kvClient.scanByStartEnd(slot, startKey, endKey, prefix, batch, Sort.DESC, includeStartKey);
            loop ++;
            if (scan.isEmpty()) {
                return result;
            }
            for (KeyValue keyValue : scan) {
                if (keyValue == null || keyValue.getValue() == null) {
                    continue;
                }
                startKey = keyValue.getKey();
                byte[] member = keyDesign.decodeZSetMemberBySubKey1(keyValue.getKey(), key);
                boolean pass = ZSetLexUtil.checkLex(member, minLex, maxLex);
                if (!pass) {
                    continue;
                }
                if (count >= limit.getOffset()) {
                    result.add(new ZSetTuple(new BytesKey(member), null));
                }
                if (limit.getCount() > 0 && result.size() >= limit.getCount()) {
                    return result;
                }
                count++;
            }
            if (scan.size() < batch) {
                return result;
            }
        }
    }

}
