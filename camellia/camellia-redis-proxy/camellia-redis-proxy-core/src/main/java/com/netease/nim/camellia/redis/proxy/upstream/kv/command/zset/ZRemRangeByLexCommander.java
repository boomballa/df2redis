package com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset;

import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.enums.RedisCommand;
import com.netease.nim.camellia.redis.proxy.monitor.KvCacheMonitor;
import com.netease.nim.camellia.redis.proxy.reply.ErrorReply;
import com.netease.nim.camellia.redis.proxy.reply.IntegerReply;
import com.netease.nim.camellia.redis.proxy.reply.MultiBulkReply;
import com.netease.nim.camellia.redis.proxy.reply.Reply;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.NoOpResult;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.Result;
import com.netease.nim.camellia.redis.proxy.upstream.kv.buffer.WriteBufferValue;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.RedisZSet;
import com.netease.nim.camellia.redis.proxy.upstream.kv.cache.ZSetLRUCache;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.CommanderConfig;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetLex;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetLimit;
import com.netease.nim.camellia.redis.proxy.upstream.kv.command.zset.utils.ZSetTuple;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.EncodeVersion;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyMeta;
import com.netease.nim.camellia.redis.proxy.upstream.kv.meta.KeyType;
import com.netease.nim.camellia.redis.proxy.util.ErrorLogCollector;
import com.netease.nim.camellia.tools.utils.BytesKey;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * ZREMRANGEBYLEX key min max
 * <p>
 * Created by caojiajun on 2024/5/8
 */
public class ZRemRangeByLexCommander extends ZRangeByLex0Commander {

    public ZRemRangeByLexCommander(CommanderConfig commanderConfig) {
        super(commanderConfig);
    }

    @Override
    public RedisCommand redisCommand() {
        return RedisCommand.ZREMRANGEBYLEX;
    }

    @Override
    protected boolean parse(Command command) {
        byte[][] objects = command.getObjects();
        return objects.length == 4;
    }

    @Override
    protected Reply execute(int slot, Command command) {
        byte[][] objects = command.getObjects();
        byte[] key = objects[1];
        KeyMeta keyMeta = keyMetaServer.getKeyMeta(slot, key);
        if (keyMeta == null) {
            return IntegerReply.REPLY_0;
        }
        if (keyMeta.getKeyType() != KeyType.zset) {
            return ErrorReply.WRONG_TYPE;
        }

        EncodeVersion encodeVersion = keyMeta.getEncodeVersion();

        ZSetLex minLex;
        ZSetLex maxLex;
        try {
            minLex = ZSetLex.fromLex(objects[2]);
            maxLex = ZSetLex.fromLex(objects[3]);
            if (minLex == null || maxLex == null) {
                return new ErrorReply("ERR min or max not valid string range item");
            }
        } catch (Exception e) {
            ErrorLogCollector.collect(ZRemRangeByLexCommander.class, "zremrangebylex command syntax error, illegal min/max lex");
            return ErrorReply.SYNTAX_ERROR;
        }
        if (minLex.isMax() || maxLex.isMin()) {
            return MultiBulkReply.EMPTY;
        }

        KvCacheMonitor.Type type = null;

        Map<BytesKey, Double> removedMembers = null;
        Result result = null;
        byte[] cacheKey = keyDesign.cacheKey(keyMeta, key);

        WriteBufferValue<RedisZSet> bufferValue = zsetWriteBuffer.get(cacheKey);
        if (bufferValue != null) {
            RedisZSet zSet = bufferValue.getValue();
            removedMembers = zSet.zremrangeByLex(minLex, maxLex);
            //
            type = KvCacheMonitor.Type.write_buffer;
            KvCacheMonitor.writeBuffer(cacheConfig.getNamespace(), redisCommand().strRaw());
            if (removedMembers != null && removedMembers.isEmpty()) {
                return IntegerReply.REPLY_0;
            }
            result = zsetWriteBuffer.put(cacheKey, zSet);
        }

        if (cacheConfig.isZSetLocalCacheEnable()) {
            ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();

            if (removedMembers == null) {
                removedMembers = zSetLRUCache.zremrangeByLex(slot, cacheKey, minLex, maxLex);
                if (removedMembers != null) {
                    type = KvCacheMonitor.Type.local_cache;
                    KvCacheMonitor.localCache(cacheConfig.getNamespace(), redisCommand().strRaw());
                }
                if (removedMembers != null && removedMembers.isEmpty()) {
                    return IntegerReply.REPLY_0;
                }
            } else {
                zSetLRUCache.zremrangeByLex(slot, cacheKey, minLex, maxLex);
            }

            if (removedMembers == null) {
                boolean hotKey = zSetLRUCache.isHotKey(key, redisCommand());
                if (hotKey) {
                    RedisZSet zSet = loadLRUCache(slot, keyMeta, key);
                    if (zSet != null) {
                        //
                        type = KvCacheMonitor.Type.kv_store;
                        KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
                        //
                        zSetLRUCache.putZSetForWrite(slot, cacheKey, zSet);
                        //
                        removedMembers = zSet.zremrangeByLex(minLex, maxLex);

                        if (removedMembers != null && removedMembers.isEmpty()) {
                            return IntegerReply.REPLY_0;
                        }
                    }
                }
            }
            if (result == null) {
                RedisZSet zSet = zSetLRUCache.getForWrite(slot, cacheKey);
                if (zSet != null) {
                    result = zsetWriteBuffer.put(cacheKey, zSet.duplicate());
                }
            }
        }

        if (result == null) {
            result = NoOpResult.INSTANCE;
        }

        if (type == null) {
            KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());
        }

        if (encodeVersion == EncodeVersion.version_0) {
            if (removedMembers == null) {
                removedMembers = new HashMap<>();
                List<ZSetTuple> list = zrangeByLexVersion0(slot, keyMeta, key, minLex, maxLex, ZSetLimit.NO_LIMIT, true);
                for (ZSetTuple tuple : list) {
                    removedMembers.put(tuple.getMember(), tuple.getScore());
                }
            }
            return zremVersion0(slot, keyMeta, key, cacheKey, removedMembers, result);
        }

        if (encodeVersion == EncodeVersion.version_1) {
            if (removedMembers == null) {
                RedisZSet zSet = loadLRUCache(slot, keyMeta, key);
                if (zSet != null) {
                    KvCacheMonitor.kvStore(cacheConfig.getNamespace(), redisCommand().strRaw());

                    if (cacheConfig.isZSetLocalCacheEnable()) {
                        ZSetLRUCache zSetLRUCache = cacheConfig.getZSetLRUCache();
                        //
                        zSetLRUCache.putZSetForWrite(slot, cacheKey, zSet);
                        //
                    }
                    removedMembers = zSet.zremrangeByLex(minLex, maxLex);
                } else {
                    return ErrorReply.INTERNAL_ERROR;
                }
            }
            return zremVersion1(slot, keyMeta, key, cacheKey, removedMembers.keySet(), false, result);
        }

        return ErrorReply.INTERNAL_ERROR;
    }

}
