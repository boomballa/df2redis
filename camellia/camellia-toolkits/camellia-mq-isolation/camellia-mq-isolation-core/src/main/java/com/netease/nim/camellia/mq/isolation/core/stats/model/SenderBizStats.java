package com.netease.nim.camellia.mq.isolation.core.stats.model;

/**
 * Created by caojiajun on 2024/2/6
 */
public class SenderBizStats {
    private String namespace;
    private String bizId;
    private long count;
    private long timestamp;

    public String getNamespace() {
        return namespace;
    }

    public void setNamespace(String namespace) {
        this.namespace = namespace;
    }

    public String getBizId() {
        return bizId;
    }

    public void setBizId(String bizId) {
        this.bizId = bizId;
    }

    public long getCount() {
        return count;
    }

    public void setCount(long count) {
        this.count = count;
    }

    public long getTimestamp() {
        return timestamp;
    }

    public void setTimestamp(long timestamp) {
        this.timestamp = timestamp;
    }
}
