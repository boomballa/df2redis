package com.netease.nim.camellia.tools.cache;

import com.googlecode.concurrentlinkedhashmap.ConcurrentLinkedHashMap;
import com.netease.nim.camellia.tools.utils.CamelliaMapUtils;
import com.netease.nim.camellia.tools.utils.LockMap;

import java.util.*;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Created by caojiajun on 2023/5/10
 */
public class NamespaceCamelliaLocalCache {

    private final Map<String, CamelliaLocalCache> map;
    private final int capacity;
    private final boolean putLock;
    private final LockMap lockMap = new LockMap();

    /**
     * 如果namespaceCapacity小于等于0，则namespace没有上限
     * @param namespaceCapacity namespaceCapacity
     * @param capacity CamelliaLocalCache的capacity
     * @param putLock putLock
     */
    public NamespaceCamelliaLocalCache(int namespaceCapacity, int capacity, boolean putLock) {
        if (namespaceCapacity > 0) {
            this.map = new ConcurrentLinkedHashMap.Builder<String, CamelliaLocalCache>()
                    .initialCapacity(namespaceCapacity)
                    .maximumWeightedCapacity(namespaceCapacity)
                    .build();
        } else {
            this.map = new ConcurrentHashMap<>();
        }
        this.capacity = capacity;
        this.putLock = putLock;
    }

    private CamelliaLocalCache get(String namespace) {
        return CamelliaMapUtils.computeIfAbsent(map, namespace, n -> new CamelliaLocalCache(capacity));
    }

    /**
     * 添加缓存
     */
    public void put(String namespace, Object key, Object value, long expireMillis) {
        CamelliaLocalCache cache = get(namespace);
        if (putLock) {
            synchronized (lockMap.getLockObj(namespace)) {
                cache.put("", key, value, expireMillis);
            }
        } else {
            cache.put("", key, value, expireMillis);
        }
    }

    /**
     * 添加缓存
     */
    public void put(String namespace, Object key, Object value, int expireSeconds) {
        CamelliaLocalCache cache = get(namespace);
        if (putLock) {
            synchronized (lockMap.getLockObj(namespace)) {
                cache.put("", key, value, expireSeconds);
            }
        } else {
            cache.put("", key, value, expireSeconds);
        }
    }

    /**
     * 添加缓存（检查是否第一次）
     */
    public boolean putIfAbsent(String namespace, Object key, Object value, long expireMillis) {
        CamelliaLocalCache cache = get(namespace);
        if (putLock) {
            synchronized (lockMap.getLockObj(namespace)) {
                return cache.putIfAbsent("", key, value, expireMillis);
            }
        } else {
            return cache.putIfAbsent("", key, value, expireMillis);
        }
    }

    /**
     * 添加缓存（检查是否第一次）
     */
    public boolean putIfAbsent(String namespace, Object key, Object value, int expireSeconds) {
        CamelliaLocalCache cache = get(namespace);
        if (putLock) {
            synchronized (lockMap.getLockObj(namespace)) {
                return cache.putIfAbsent("", key, value, expireSeconds);
            }
        } else {
            return cache.putIfAbsent("", key, value, expireSeconds);
        }
    }

    /**
     * 获取缓存
     */
    public CamelliaLocalCache.ValueWrapper get(String namespace, Object key) {
        return get(namespace).get("", key);
    }

    /**
     * 获取缓存
     */
    public <T> T get(String namespace, Object key, Class<T> clazz) {
        return get(namespace).get("", key, clazz);
    }

    /**
     * 删除缓存
     */
    public Object evict(String namespace, Object key) {
        return get(namespace).evict("", key);
    }

    /**
     * 获取ttl
     */
    public boolean exists(String namespace, Object key) {
        return get(namespace).exists("", key);
    }

    /**
     * 获取ttl
     */
    public long ttl(String namespace, Object key) {
        return get(namespace).ttl("", key);
    }

    /**
     * 获取namespace列表
     */
    public Set<String> namespaceSet() {
        return new HashSet<>(map.keySet());
    }

    /**
     * 获取values
     */
    public List<Object> values(String namespace) {
        return get(namespace).values();
    }
}
