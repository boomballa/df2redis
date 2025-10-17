package com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset;

import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.enums.RedisCommand;
import com.netease.nim.camellia.redis.proxy.monitor.KvCacheMonitor;
import com.netease.nim.camellia.redis.proxy.reply.BulkReply;
import com.netease.nim.camellia.redis.proxy.reply.ErrorReply;
import com.netease.nim.camellia.redis.proxy.reply.MultiBulkReply;
import com.netease.nim.camellia.redis.proxy.reply.Reply;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.WriteBufferValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.RedisZSet;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ValueWrapper;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ZSetLRUCache;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.CommanderConfig;
import com.netease.nim.camellia.redis.proxy.upstream.kv.domain.Index;
import com.netease.nim.camellia.redis.proxy.upstream.kv.kv.KeyValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.EncodeVersion;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyMeta;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyType;
import com.netease.nim.camellia.redis.proxy.util.Utils;
import com.netease.nim.camellia.tools.utils.BytesKey;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * ZMSCORE key member [member ...]
 * <p>
 * Created by caojiajun on 2024/6/6
 */
public class ZMScoreCommander extends ZSet0Commander {

    public ZMScoreCommander(CommanderConfig commanderConfig) {
        super(commanderConfig);
    }

    @Override
    public RedisCommand redisCommand() {
        return RedisCommand.ZMSCORE;
    }

    @Override
    protected boolean parse(Command command) {
        byte[][] objects = command.getObjects();
        return objects.length >= 3;
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

        List<BytesKey> members = new ArrayList<>();
        for (int i=2; i<objects.length; i++) {
            members.add(new BytesKey(objects[i]));
        }

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            List<Double> zmscore = zSet.zmscore(members);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return toReply(zmscore);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);
            if (zSet != null) {
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return toReply(zSet.zmscore(members));
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

        List<BytesKey> members = new ArrayList<>();
        for (int i=2; i<objects.length; i++) {
            members.add(new BytesKey(objects[i]));
        }

        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            List<Double> zmscore = zSet.zmscore(members);
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            return toReply(zmscore);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            RedisZSet zSet = zSetLRUCache.getForRead(slot, cacheKey);
            if (zSet != null) {
                KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                return toReply(zSet.zmscore(members));
            }

            boolean hotKey = zSetLRUCache.isHotKey(key, redisCommand());

            if (hotKey) {
                zSet = loadLRUCache(slot, keyMeta, key);
                if (zSet != null) {
                    zSetLRUCache.putZSetForRead(slot, cacheKey, zSet);
                    KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
                    return toReply(zSet.zmscore(members));
                }
            }
        }

        EncodeVersion encodeVersion = keyMeta.getEncodeVersion();

        if (encodeVersion == EncodeVersion.version_0) {
            KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
            return zmscoreFromKv(slot, keyMeta, key, members);
        }

        if (encodeVersion == EncodeVersion.version_1) {
            byte[][] cmd = new byte[objects.length][];
            System.arraycopy(objects, 0, cmd, 0, cmd.length);
            cmd[1] = cacheKey;
            for (int i=2; i<cmd.length; i++) {
                Index index = Index.fromRaw(cmd[i]);
                cmd[i] = index.getRef();
            }
            KvCacheMonitor.redisCache(cacheConfig.getNamespace(), redisCommand().strRaw());
            return sync(storageRedisTemplate.sendCommand(new Command(cmd)));
        }
        return ErrorReply.INTERNAL_ERROR;
    }

    private Reply zmscoreFromKv(int slot, KeyMeta keyMeta, byte[] key, List<BytesKey> members) {
        List<byte[]> subKeys = new ArrayList<>(members.size());
        for (BytesKey member : members) {
            subKeys.add(keyDesign.zsetMemberSubKey1(keyMeta, key, member.getKey()));
        }
        List<KeyValue> keyValues = kvClient.batchGet(slot, subKeys.toArray(new byte[0][0]));
        Map<BytesKey, Double> map = new HashMap<>();
        for (KeyValue keyValue : keyValues) {
            if (keyValue == null || keyValue.getValue() == null) {
                continue;
            }
            double score = Utils.bytesToDouble(keyValue.getValue());
            map.put(new BytesKey(keyDesign.decodeZSetMemberBySubKey1(keyValue.getKey(), key)), score);
        }
        List<Double> list = new ArrayList<>(members.size());
        for (BytesKey member : members) {
            list.add(map.get(member));
        }
        return toReply(list);
    }

    private Reply toReply(List<Double> list) {
        Reply[] replies = new Reply[list.size()];
        for (int i=0; i<list.size(); i++) {
            Double v = list.get(i);
            if (v == null) {
                replies[i] = BulkReply.NIL_REPLY;
            } else {
                replies[i] = new BulkReply(Utils.doubleToBytes(v));
            }
        }
        return new MultiBulkReply(replies);
    }
}
