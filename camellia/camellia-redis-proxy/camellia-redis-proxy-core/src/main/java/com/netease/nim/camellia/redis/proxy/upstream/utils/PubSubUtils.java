package com.netease.nim.camellia.redis.proxy.upstream.utils;

import com.netease.nim.camellia.core.model.Resource;
import com.netease.nim.camellia.redis.proxy.command.CommandTaskQueue;
import com.netease.nim.camellia.redis.proxy.command.Command;
import com.netease.nim.camellia.redis.proxy.command.CommandContext;
import com.netease.nim.camellia.redis.proxy.monitor.ProxyMonitorCollector;
import com.netease.nim.camellia.redis.proxy.monitor.UpstreamFailMonitor;
import com.netease.nim.camellia.redis.proxy.netty.ChannelInfo;
import com.netease.nim.camellia.redis.proxy.reply.*;
import com.netease.nim.camellia.redis.proxy.upstream.connection.RedisConnection;
import com.netease.nim.camellia.redis.proxy.plugin.converter.KeyConverter;
import com.netease.nim.camellia.redis.proxy.enums.RedisCommand;
import com.netease.nim.camellia.redis.proxy.util.ErrorLogCollector;
import com.netease.nim.camellia.redis.proxy.util.Utils;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.CompletableFuture;

/**
 *
 * Created by caojiajun on 2020/9/29
 */
public class PubSubUtils {

    public static void updateChannelInfo(Command command) {
        byte[][] objects = command.getObjects();
        if (objects == null || objects.length <= 1) return;
        RedisCommand redisCommand = command.getRedisCommand();
        ChannelInfo channelInfo = command.getChannelInfo();
        if (redisCommand == RedisCommand.SUBSCRIBE || redisCommand == RedisCommand.SSUBSCRIBE || redisCommand == RedisCommand.PSUBSCRIBE) {
            for (int j = 1; j < objects.length; j++) {
                byte[] channel = objects[j];
                if (redisCommand == RedisCommand.SUBSCRIBE) {
                    channelInfo.addSubscribeChannels(channel);
                } else if (redisCommand == RedisCommand.PSUBSCRIBE) {
                    channelInfo.addPSubscribeChannels(channel);
                } else {
                    channelInfo.addSSubscribeChannels(channel);
                }
            }
        } else if (redisCommand == RedisCommand.UNSUBSCRIBE || redisCommand == RedisCommand.SUNSUBSCRIBE || redisCommand == RedisCommand.PUNSUBSCRIBE) {
            for (int j = 1; j < command.getObjects().length; j++) {
                byte[] channel = command.getObjects()[j];
                if (redisCommand == RedisCommand.UNSUBSCRIBE) {
                    channelInfo.removeSubscribeChannels(channel);
                } else if (redisCommand == RedisCommand.PUNSUBSCRIBE) {
                    channelInfo.removePSubscribeChannels(channel);
                } else {
                    channelInfo.removeSSubscribeChannels(channel);
                }
            }
        }
    }

    public static void sendByBindConnection(Resource resource, RedisConnection connection, CommandTaskQueue taskQueue,
                                            Command command) {
        taskQueue.clear();
        sendByBindConnection0(resource, connection, taskQueue, command, command.getRedisCommand());
    }

    private static void sendByBindConnection0(Resource resource, RedisConnection connection, CommandTaskQueue taskQueue,
                                             Command command, RedisCommand redisCommand) {
        List<CompletableFuture<Reply>> futures = new ArrayList<>();
        if (connection.queueSize() < 64) {
            for (int j = 0; j < 128; j++) {
                CompletableFuture<Reply> completableFuture = new CompletableFuture<>();
                completableFuture.thenAccept(reply -> {
                    if (connection.queueSize() < 64 && connection.isValid()) {
                        sendByBindConnection0(resource, connection, taskQueue, null, redisCommand);
                    }
                    if (reply instanceof ErrorReply) {
                        String error = ((ErrorReply) reply).getError();
                        if (error != null && error.startsWith("MOVED")) {
                            reply = ErrorReply.COMMAND_MOVED_BY_UPSTREAM_SERVER;
                        }
                    }
                    //parse reply must before send reply to connection
                    SubscribeCount subscribeCount = tryGetSubscribeChannelCount(reply);
                    taskQueue.reply(redisCommand, reply, false, true);
                    //after send reply, update channel subscribe status
                    if (subscribeCount != null && subscribeCount.count != null) {
                        ChannelInfo channelInfo = taskQueue.getChannelInfo();
                        if (subscribeCount.shardPubSub) {
                            channelInfo.updateSSubscribeCount(subscribeCount.count);
                        } else {
                            channelInfo.updateSubscribeCount(subscribeCount.count);
                        }
                        if (channelInfo.isSubscribeCountZero()) {
                            channelInfo.setInSubscribe(false);
                        }
                    }
                    //monitor
                    if (ProxyMonitorCollector.isMonitorEnable()) {
                        UpstreamFailMonitor.stats(resource.getUrl(), command == null ? "pubsub" : command.getName(), reply);
                    }
                });
                futures.add(completableFuture);
            }
        }
        if (command != null) {
            connection.sendCommand(Collections.singletonList(command), futures);
        } else {
            connection.sendCommand(Collections.emptyList(), futures);
        }
    }

    public static void checkKeyConverter(RedisCommand redisCommand, CommandContext commandContext, KeyConverter keyConverter, Reply reply) {
        if (keyConverter != null) {
            if (reply instanceof MultiBulkReply) {
                Reply[] replies = ((MultiBulkReply) reply).getReplies();
                if (replies.length > 1) {
                    if (replies[0] instanceof BulkReply) {
                        String type = Utils.bytesToString(((BulkReply) replies[0]).getRaw());
                        if (type.equalsIgnoreCase("psubscribe") || type.equalsIgnoreCase("subscribe") || type.equalsIgnoreCase("ssubscribe")
                                || type.equalsIgnoreCase("unsubscribe") || type.equalsIgnoreCase("punsubscribe") || type.equalsIgnoreCase("sunsubscribe")
                                || type.equalsIgnoreCase("message") || type.equalsIgnoreCase("smessage")) {
                            if (replies.length == 3) {
                                Reply reply1 = replies[1];
                                if (reply1 instanceof BulkReply) {
                                    byte[] raw = ((BulkReply) reply1).getRaw();
                                    byte[] convert = keyConverter.reverseConvert(commandContext, redisCommand, raw);
                                    ((BulkReply) reply1).updateRaw(convert);
                                }
                            }
                        } else if (type.equalsIgnoreCase("pmessage")) {
                            if (replies.length == 4) {
                                Reply reply1 = replies[1];
                                if (reply1 instanceof BulkReply) {
                                    byte[] raw = ((BulkReply) reply1).getRaw();
                                    byte[] convert = keyConverter.reverseConvert(commandContext, redisCommand, raw);
                                    ((BulkReply) reply1).updateRaw(convert);
                                }
                                Reply reply2 = replies[2];
                                if (reply2 instanceof BulkReply) {
                                    byte[] raw = ((BulkReply) reply2).getRaw();
                                    byte[] convert = keyConverter.reverseConvert(commandContext, redisCommand, raw);
                                    ((BulkReply) reply2).updateRaw(convert);
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    public static boolean isShardPubSub(RedisCommand redisCommand) {
        return redisCommand == RedisCommand.SPUBLISH || redisCommand == RedisCommand.SSUBSCRIBE || redisCommand == RedisCommand.SUNSUBSCRIBE;
    }

    private static SubscribeCount tryGetSubscribeChannelCount(Reply reply) {
        try {
            if (reply instanceof MultiBulkReply) {
                Reply[] replies = ((MultiBulkReply) reply).getReplies();
                if (replies.length > 0) {
                    Reply firstReply = replies[0];
                    if (firstReply instanceof BulkReply) {
                        byte[] raw = ((BulkReply) firstReply).getRaw();
                        String str = Utils.bytesToString(raw);
                        if (str.equalsIgnoreCase(RedisCommand.SUBSCRIBE.strRaw())) {
                            return new SubscribeCount(false, subscribeChannelCount(replies));
                        } else if (str.equalsIgnoreCase(RedisCommand.UNSUBSCRIBE.strRaw())) {
                            return new SubscribeCount(false, subscribeChannelCount(replies));
                        } else if (str.equalsIgnoreCase(RedisCommand.PSUBSCRIBE.strRaw())) {
                            return new SubscribeCount(false, subscribeChannelCount(replies));
                        } else if (str.equalsIgnoreCase(RedisCommand.PUNSUBSCRIBE.strRaw())) {
                            return new SubscribeCount(false, subscribeChannelCount(replies));
                        } else if (str.equalsIgnoreCase(RedisCommand.SSUBSCRIBE.strRaw())) {
                            return new SubscribeCount(true, subscribeChannelCount(replies));
                        } else if (str.equalsIgnoreCase(RedisCommand.SUNSUBSCRIBE.strRaw())) {
                            return new SubscribeCount(true, subscribeChannelCount(replies));
                        }
                    }
                }
            }
            return null;
        } catch (Exception e) {
            ErrorLogCollector.collect(PubSubUtils.class, "tryGetSubscribeChannelCount error", e);
            return null;
        }
    }

    private static class SubscribeCount {
        boolean shardPubSub;
        Long count;

        public SubscribeCount(boolean shardPubSub, Long count) {
            this.shardPubSub = shardPubSub;
            this.count = count;
        }
    }

    private static Long subscribeChannelCount(Reply[] replies) {
        if (replies != null && replies.length >= 2) {
            Reply reply = replies[2];
            if (reply instanceof IntegerReply) {
                return ((IntegerReply) reply).getInteger();
            }
        }
        return null;
    }
}
