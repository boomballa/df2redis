package com.netease.nim.camellia.redis.proxy.upstream;

import com.netease.nim.camellia.core.model.Resource;
import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.reply.Reply;
import com.netease.nim.camellia.redis.proxy.upstream.connection.RedisConnection;
import com.netease.nim.camellia.redis.proxy.upstream.connection.RedisConnectionAddr;
import com.netease.nim.camellia.redis.proxy.upstream.connection.RedisConnectionHub;
import com.netease.nim.camellia.redis.proxy.upstream.connection.RedisConnectionStatus;

import java.util.List;
import java.util.concurrent.CompletableFuture;

/**
 *
 * Created by caojiajun on 2019/12/19.
 */
public interface IUpstreamClient {

    /**
     * send commands to upstream
     * @param db db
     * @param commands commands
     * @param futureList future list
     */
    void sendCommand(int db, List<Command> commands, List<CompletableFuture<Reply>> futureList);

    /**
     * start method
     */
    void start();

    /**
     * preheat method
     */
    void preheat();

    /**
     * is valid
     * @return result
     */
    boolean isValid();

    /**
     * shutdown
     */
    void shutdown();

    /**
     * get resource
     * @return resource
     */
    Resource getResource();

    /**
     * renew
     */
    void renew();

    /**
     * get status of addr
     * @param addr addr
     * @return status
     */
    default RedisConnectionStatus getStatus(RedisConnectionAddr addr) {
        if (addr == null) return RedisConnectionStatus.INVALID;
        RedisConnection redisConnection = RedisConnectionHub.getInstance().get(this, addr);
        if (redisConnection == null) {
            return RedisConnectionStatus.INVALID;
        }
        return redisConnection.getStatus();
    }
}
