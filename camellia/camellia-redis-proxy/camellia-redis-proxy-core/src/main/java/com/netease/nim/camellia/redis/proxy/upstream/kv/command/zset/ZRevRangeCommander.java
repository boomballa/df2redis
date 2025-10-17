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
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetRank;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetTuple;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetTupleUtils;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetWithScoresUtils;
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
 * ZREVRANGE key start stop [WITHSCORES]
 * <p>
 * Created by caojiajun on 2024/4/11
 */
public class ZRevRangeCommander extends ZSet0Commander {

    public ZRevRangeCommander(CommanderConfig commanderConfig) {
        super(commanderConfig);
    }

    @Override
    public RedisCommand redisCommand() {
        return RedisCommand.ZREVRANGE;
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
        boolean withScores = ZSetWithScoresUtils.isWithScores(objects, 4);
        if (objects.length == 5 && !withScores) {
            ErrorLogCollector.collect(ZRevRangeCommander.class, "zrevrange command syntax error, illegal withscore arg");
            return ErrorReply.SYNTAX_ERROR;
        }

        int start = (int) Utils.bytesToNum(objects[2]);
        int stop = (int) Utils.bytesToNum(objects[3]);

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            List<ZSetTuple> list = zSet.zrevrange(start, stop);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return ZSetTupleUtils.toReply(list, withScores);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);

            if (zSet != null) {
                List<ZSetTuple> list = zSet.zrevrange(start, stop);
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return ZSetTupleUtils.toReply(list, withScores);
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
        boolean withScores = ZSetWithScoresUtils.isWithScores(objects, 4);
        if (objects.length == 5 && !withScores) {
            ErrorLogCollector.collect(ZRevRangeCommander.class, "zrevrange command syntax error, illegal withscore arg");
            return ErrorReply.SYNTAX_ERROR;
        }

        int start = (int) Utils.bytesToNum(objects[2]);
        int stop = (int) Utils.bytesToNum(objects[3]);

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            List<ZSetTuple> list = zSet.zrevrange(start, stop);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return ZSetTupleUtils.toReply(list, withScores);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);

            if (zSet != null) {
                List<ZSetTuple> list = zSet.zrevrange(start, stop);
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return ZSetTupleUtils.toReply(list, withScores);
            }

            boolean hotKey = zSetLRUCache.isHotKey(key, redisCommand());

            if (hotKey) {
                zSet = loadLRUCache(slot, keyMeta, key);
                if (zSet != null) {
                    //
                    zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
                    //
                    List<ZSetTuple> list = zSet.zrevrange(start, stop);

                    KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());

                    return ZSetTupleUtils.toReply(list, withScores);
                }
            }
        }

        EncodeVersion encodeVersion = keyMeta.getEncodeVersion();

        if (encodeVersion == EncodeVersion.version_0) {
            KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
            if (!kvClient.supportReverseScan()) {
                return zrevrangeVersion0NotSupportReverseScan(slot, keyMeta, key, cacheKey, start, stop, withScores);
            }
            return zrevrangeVersion0(slot, keyMeta, key, start, stop, withScores);
        }

        if (encodeVersion == EncodeVersion.version_1) {
            KvCacheMonitor.redisCache(cacheConfig.getNamespace(), redisCommand().strRaw());
            return zrangeVersion1(slot, keyMeta, key, cacheKey, objects, withScores);
        }

        return ErrorReply.INTERNAL_ERROR;
    }

    private Reply zrevrangeVersion0NotSupportReverseScan(int slot, KeyMeta keyMeta, byte[] key, byte[] cacheKey, int start, int stop, boolean withScores) {
        RedisZSet zSet = loadLRUCache(slot, keyMeta, key);
        if (zSet == null) {
            return Utils.commandNotSupport(RedisCommand.ZREVRANGE);
        }
        //
        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();
            zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
        }
        //
        List<ZSetTuple> list = zSet.zrevrange(start, stop);

        return ZSetTupleUtils.toReply(list, withScores);
    }

    private Reply zrevrangeVersion0(int slot, KeyMeta keyMeta, byte[] key, int start, int stop, boolean withScores) {
        int size = BytesUtils.toInt(keyMeta.getExtra());
        ZSetRank rank = new ZSetRank(start, stop, size);
        if (rank.isEmptyRank()) {
            return MultiBulkReply.EMPTY;
        }
        start = rank.getStart();
        stop = rank.getStop();

        byte[] startKey = keyDesign.zsetMemberSubKey2(keyMeta, key, new byte[0], new byte[0]);
        List<ZSetTuple> list = zrevrange0(slot, key, BytesUtils.nextBytes(startKey), startKey, start, stop, withScores);

        return ZSetTupleUtils.toReply(list, withScores);
    }

    private List<ZSetTuple> zrevrange0(int slot, byte[] key, byte[] startKey, byte[] prefix, int start, int stop, boolean withScores) {
        List<ZSetTuple> list = new ArrayList<>();
        int scanBatch = kvConfig.scanBatch();
        int count = 0;
        while (true) {
            int limit = Math.min(stop - count + 1, scanBatch);
            List<KeyValue> scan = kvClient.scanByPrefix(slot, startKey, prefix, limit, Sort.DESC, false);
            if (scan.isEmpty()) {
                return list;
            }
            for (KeyValue keyValue : scan) {
                if (keyValue == null || keyValue.getValue() == null) {
                    continue;
                }
                startKey = keyValue.getKey();
                if (count >= start) {
                    byte[] member = keyDesign.decodeZSetMemberBySubKey2(keyValue.getKey(), key);
                    if (withScores) {
                        double score = keyDesign.decodeZSetScoreBySubKey2(keyValue.getKey(), key);
                        list.add(new ZSetTuple(new BytesKey(member), score));
                    } else {
                        list.add(new ZSetTuple(new BytesKey(member), null));
                    }
                }
                if (count >= stop) {
                    return list;
                }
                count++;
            }
            if (scan.size() < limit) {
                return list;
            }
        }
    }
}
