package com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset;

import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.enums.RedisCommand;
import com.netease.nim.camellia.redis.proxy.monitor.KvCacheMonitor;
import com.netease.nim.camellia.redis.proxy.reply.*;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.WriteBufferValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.RedisZSet;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ValueWrapper;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ZSetLRUCache;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.CommanderConfig;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetTuple;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetWithScoresUtils;
import com.netease.nim.camellia.redis.proxy.upstream.kv.domain.Index;
import com.netease.nim.camellia.redis.proxy.upstream.kv.kv.KeyValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.kv.Sort;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.EncodeVersion;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyMeta;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyType;
import com.netease.nim.camellia.redis.proxy.upstream.kv.utils.BytesUtils;
import com.netease.nim.camellia.redis.proxy.util.Utils;
import com.netease.nim.camellia.tools.utils.BytesKey;
import com.netease.nim.camellia.tools.utils.Pair;

import java.util.Arrays;
import java.util.List;

/**
 * ZREVRANK key member [WITHSCORE]
 * <p>
 * Created by caojiajun on 2024/6/6
 */
public class ZRevRankCommander extends ZSet0Commander {

    public ZRevRankCommander(CommanderConfig commanderConfig) {
        super(commanderConfig);
    }

    @Override
    public RedisCommand redisCommand() {
        return RedisCommand.ZREVRANK;
    }

    @Override
    protected boolean parse(Command command) {
        byte[][] objects = command.getObjects();
        return objects.length == 3 || objects.length == 4;
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
            return BulkReply.NIL_REPLY;
        }
        if (keyMeta.getKeyType() != KeyType.zset) {
            return ErrorReply.WRONG_TYPE;
        }

        BytesKey member = new BytesKey(objects[2]);
        boolean withScores = ZSetWithScoresUtils.isWithScores(objects, 3);

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            Pair<Integer, ZSetTuple> zrank = zSet.zrevrank(member);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return toReply(zrank, withScores);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();
            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);

            if (zSet != null) {
                Pair<Integer, ZSetTuple> zrank = zSet.zrevrank(member);
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return toReply(zrank, withScores);
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
            return BulkReply.NIL_REPLY;
        }
        if (keyMeta.getKeyType() != KeyType.zset) {
            return ErrorReply.WRONG_TYPE;
        }

        BytesKey member = new BytesKey(objects[2]);
        boolean withScores = ZSetWithScoresUtils.isWithScores(objects, 3);

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            Pair<Integer, ZSetTuple> zrank = zSet.zrevrank(member);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return toReply(zrank, withScores);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();
            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);

            if (zSet != null) {
                Pair<Integer, ZSetTuple> zrank = zSet.zrevrank(member);
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return toReply(zrank, withScores);
            }

            boolean hotKey = zSetLRUCache.isHotKey(key, redisCommand());

            if (hotKey) {
                zSet = loadLRUCache(slot, keyMeta, key);
                if (zSet != null) {
                    zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
                    Pair<Integer, ZSetTuple> zrank = zSet.zrevrank(member);
                    return toReply(zrank, withScores);
                }
            }
        }

        EncodeVersion encodeVersion = keyMeta.getEncodeVersion();

        if (encodeVersion == EncodeVersion.version_0) {
            KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
            return zrevrank(slot, keyMeta, key, cacheKey, member, withScores);
        }

        if (encodeVersion == EncodeVersion.version_1) {
            byte[][] cmd = new byte[objects.length][];
            System.arraycopy(objects, 0, cmd, 0, cmd.length);
            cmd[1] = cacheKey;
            Index index = Index.fromRaw(cmd[2]);
            cmd[2] = index.getRef();
            KvCacheMonitor.redisCache(cacheConfig.getNamespace(), redisCommand().strRaw());
            return sync(storageRedisTemplate.sendCommand(new Command(cmd)));
        }

        return ErrorReply.INTERNAL_ERROR;
    }


    private Reply zrevrank(int slot, KeyMeta keyMeta, byte[] key, byte[] cacheKey, BytesKey member, boolean withScores) {
        if (kvClient.supportReverseScan()) {
            Pair<Integer, ZSetTuple> result = zrevrankFromKv(slot, keyMeta, key, member);
            return toReply(result, withScores);
        } else {
            RedisZSet zSet = loadLRUCache(slot, keyMeta, key);
            if (zSet != null) {
                if (cacheConfig.isZSetLocalCacheEnable()) {
                    cacheConfig.getZSetLRUCache().putZSetForRead(slot, cacheKey, zSet);
                }
                Pair<Integer, ZSetTuple> result = zSet.zrevrank(member);
                return toReply(result, withScores);
            } else {
                return Utils.commandNotSupport(RedisCommand.ZREVRANK);
            }
        }
    }

    private Pair<Integer, ZSetTuple> zrevrankFromKv(int slot, KeyMeta keyMeta, byte[] key, BytesKey member) {
        int scanBatch = kvConfig.scanBatch();
        byte[] startKey = keyDesign.zsetMemberSubKey2(keyMeta, key, new byte[0], new byte[0]);
        byte[] prefix = startKey;
        startKey = BytesUtils.nextBytes(startKey);
        int index = 0;
        while (true) {
            List<KeyValue> scan = kvClient.scanByPrefix(slot, startKey, prefix, scanBatch, Sort.DESC, false);
            if (scan.isEmpty()) {
                return null;
            }
            for (KeyValue keyValue : scan) {
                if (keyValue == null || keyValue.getValue() == null) {
                    continue;
                }
                startKey = keyValue.getKey();
                if (Arrays.equals(keyDesign.decodeZSetMemberBySubKey2(startKey, key), member.getKey())) {
                    double score = keyDesign.decodeZSetScoreBySubKey2(keyValue.getKey(), key);
                    return new Pair<>(index, new ZSetTuple(member, score));
                }
                index++;
            }
            if (scan.size() < scanBatch) {
                return null;
            }
        }
    }

    private Reply toReply(Pair<Integer, ZSetTuple> zrank, boolean withScores) {
        if (zrank == null) {
            return BulkReply.NIL_REPLY;
        }
        if (withScores) {
            Reply[] replies = new Reply[2];
            replies[0] = IntegerReply.parse(zrank.getFirst());
            replies[1] = new BulkReply(zrank.getSecond().getMember().getKey());
            return new MultiBulkReply(replies);
        } else {
            return IntegerReply.parse(zrank.getFirst());
        }
    }

}
