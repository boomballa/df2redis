package com.netease.nim.camellia.mq.isolation.core.config;

import com.netease.nim.camellia.mq.isolation.core.mq.MqInfo;
import com.netease.nim.camellia.mq.isolation.core.mq.TopicType;

import java.util.Set;

/**
 * Created by caojiajun on 2024/2/21
 */
public class ConsumerMqInfoConfig {

    private ConsumerManagerType type;

    private Set<TopicType> excludeTopicTypeSet;

    private Set<MqInfo> excludeMqInfoSet;

    private Set<TopicType> specifyTopicTypeSet;

    private Set<MqInfo> specifyMqInfoSet;

    public ConsumerManagerType getType() {
        return type;
    }

    public void setType(ConsumerManagerType type) {
        this.type = type;
    }

    public Set<TopicType> getExcludeTopicTypeSet() {
        return excludeTopicTypeSet;
    }

    public void setExcludeTopicTypeSet(Set<TopicType> excludeTopicTypeSet) {
        this.excludeTopicTypeSet = excludeTopicTypeSet;
    }

    public Set<MqInfo> getExcludeMqInfoSet() {
        return excludeMqInfoSet;
    }

    public void setExcludeMqInfoSet(Set<MqInfo> excludeMqInfoSet) {
        this.excludeMqInfoSet = excludeMqInfoSet;
    }

    public Set<TopicType> getSpecifyTopicTypeSet() {
        return specifyTopicTypeSet;
    }

    public void setSpecifyTopicTypeSet(Set<TopicType> specifyTopicTypeSet) {
        this.specifyTopicTypeSet = specifyTopicTypeSet;
    }

    public Set<MqInfo> getSpecifyMqInfoSet() {
        return specifyMqInfoSet;
    }

    public void setSpecifyMqInfoSet(Set<MqInfo> specifyMqInfoSet) {
        this.specifyMqInfoSet = specifyMqInfoSet;
    }
}
