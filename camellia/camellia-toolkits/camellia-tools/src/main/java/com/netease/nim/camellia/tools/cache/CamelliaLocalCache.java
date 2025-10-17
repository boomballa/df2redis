package com.netease.nim.camellia.tools.cache;

import com.googlecode.concurrentlinkedhashmap.ConcurrentLinkedHashMap;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.Collection;
import java.util.List;

/**
 * 本地缓存工具类，支持设置缓存有效期，缓存空值
 * Created by caojiajun on 2018/2/27.
 */
public class CamelliaLocalCache {

    private static final Logger logger = LoggerFactory.getLogger(CamelliaLocalCache.class);

    private final ConcurrentLinkedHashMap<String, CacheBean> cache;

    private static final Object nullCache = new Object();

    public static CamelliaLocalCache DEFAULT = new CamelliaLocalCache();

    public CamelliaLocalCache() {
        this(100000);
    }

    public CamelliaLocalCache(int capacity) {
        this(capacity, capacity);
    }

    public CamelliaLocalCache(int initialCapacity, int capacity) {
        cache = new ConcurrentLinkedHashMap.Builder<String, CacheBean>()
                .initialCapacity(initialCapacity).maximumWeightedCapacity(capacity).build();
    }

    /**
     * 添加缓存
     */
    public void put(String tag, Object key, Object value, long expireMillis) {
        String uniqueKey = buildCacheKey(tag, key);
        CacheBean bean;
        if (value == null) {
            value = nullCache;
        }
        if (expireMillis <= 0) {
            bean = new CacheBean(value, Long.MAX_VALUE);
        } else {
            bean = new CacheBean(value, System.currentTimeMillis() + expireMillis);
        }
        cache.put(uniqueKey, bean);
        if (logger.isTraceEnabled()) {
            logger.trace("local cache put, tag = {}, key = {}, expireMillis = {}", tag, key, expireMillis);
        }
    }

    /**
     * 添加缓存
     */
    public void put(String tag, Object key, Object value, int expireSeconds) {
        String uniqueKey = buildCacheKey(tag, key);
        CacheBean bean;
        if (value == null) {
            value = nullCache;
        }
        if (expireSeconds <= 0) {
            bean = new CacheBean(value, Long.MAX_VALUE);
        } else {
            bean = new CacheBean(value, System.currentTimeMillis() + expireSeconds * 1000L);
        }
        cache.put(uniqueKey, bean);
        if (logger.isTraceEnabled()) {
            logger.trace("local cache put, tag = {}, key = {}, expireSeconds = {}", tag, key, expireSeconds);
        }
    }

    /**
     * 添加缓存（检查是否第一次）
     */
    public boolean putIfAbsent(String tag, Object key, Object value, long expireMillis) {
        String uniqueKey = buildCacheKey(tag, key);
        CacheBean bean;
        if (value == null) {
            value = nullCache;
        }
        if (expireMillis <= 0) {
            bean = new CacheBean(value, Long.MAX_VALUE);
        } else {
            bean = new CacheBean(value, System.currentTimeMillis() + expireMillis);
        }
        CacheBean oldBean = cache.get(uniqueKey);
        if (oldBean != null && oldBean.isExpire()) {
            cache.remove(uniqueKey);
        }
        CacheBean cacheBean = cache.putIfAbsent(uniqueKey, bean);
        if (logger.isTraceEnabled()) {
            logger.trace("local cache put, tag = {}, key = {}, expireMillis = {}", tag, key, expireMillis);
        }
        return cacheBean == null;
    }

    /**
     * 添加缓存（检查是否第一次）
     */
    public boolean putIfAbsent(String tag, Object key, Object value, int expireSeconds) {
        String uniqueKey = buildCacheKey(tag, key);
        CacheBean bean;
        if (value == null) {
            value = nullCache;
        }
        if (expireSeconds <= 0) {
            bean = new CacheBean(value, Long.MAX_VALUE);
        } else {
            bean = new CacheBean(value, System.currentTimeMillis() + expireSeconds * 1000L);
        }
        CacheBean oldBean = cache.get(uniqueKey);
        if (oldBean != null && oldBean.isExpire()) {
            cache.remove(uniqueKey);
        }
        CacheBean cacheBean = cache.putIfAbsent(uniqueKey, bean);
        if (logger.isTraceEnabled()) {
            logger.trace("local cache put, tag = {}, key = {}, expireSeconds = {}", tag, key, expireSeconds);
        }
        return cacheBean == null;
    }

    /**
     * 获取缓存
     */
    public ValueWrapper get(String tag, Object key) {
        String uniqueKey = buildCacheKey(tag, key);
        final CacheBean bean = cache.get(uniqueKey);
        ValueWrapper ret = null;
        if (bean != null) {
            if (bean.isExpire()) {
                cache.remove(uniqueKey);
            } else if (isNullCache(bean.getValue())) {
                ret = () -> null;
            } else {
                ret = bean::getValue;
            }
        }
        if (logger.isTraceEnabled()) {
            logger.trace("local cache get, tag = {}, key = {}, hit = {}", tag, key, ret != null);
        }
        return ret;
    }

    /**
     * 获取缓存
     */
    public <T> T get(String tag, Object key, Class<T> clazz) {
        String uniqueKey = buildCacheKey(tag, key);
        CacheBean bean = cache.get(uniqueKey);
        T ret = null;
        if (bean != null) {
            if (bean.isExpire()) {
                cache.remove(uniqueKey);
            } else if (isNullCache(bean.getValue())) {
                ret = null;
            } else if (clazz.isAssignableFrom(bean.getValue().getClass())) {
                ret = (T) bean.getValue();
            }
        }
        if (logger.isTraceEnabled()) {
            logger.trace("local cache get, tag = {}, key = {}, class = {}, hit = {}",
                    tag, key, clazz.getSimpleName(), ret != null);
        }
        return ret;
    }

    /**
     * 删除缓存
     */
    public Object evict(String tag, Object key) {
        String uniqueKey = buildCacheKey(tag, key);
        if (logger.isTraceEnabled()) {
            logger.trace("local cache evict, tag = {}, key = {}", tag, key);
        }
        CacheBean bean = cache.remove(uniqueKey);
        if (bean == null) {
            return null;
        }
        if (bean.isExpire()) {
            return null;
        }
        return bean.getValue();
    }

    /**
     * 是否存在
     */
    public boolean exists(String tag, Object key) {
        long ttl = ttl(tag, key);
        return ttl > 0 || ttl == -1;
    }

    /**
     * 获取ttl
     */
    public long ttl(String tag, Object key) {
        String uniqueKey = buildCacheKey(tag, key);
        CacheBean cacheBean = cache.get(uniqueKey);
        if (cacheBean == null || cacheBean.isExpire()) {
            return -2;
        }
        if (cacheBean.notTtl()) {
            return -1;
        }
        long ttl = cacheBean.expireTime - System.currentTimeMillis();
        if (ttl > 0) {
            return ttl;
        }
        return -2;
    }

    /**
     * 清空缓存
     */
    public void clear() {
        if (!cache.isEmpty()) {
            cache.clear();
        }
        if (logger.isDebugEnabled()) {
            logger.debug("local cache clear");
        }
    }

    public List<Object> values() {
        List<CacheBean> values = new ArrayList<>(cache.values());
        List<Object> list = new ArrayList<>();
        for (CacheBean bean : values) {
            if (bean.getValue() == null) {
                continue;
            }
            list.add(bean.getValue());
        }
        return list;
    }

    //是否缓存了NULL值
    private boolean isNullCache(Object value) {
        return value != null && value.equals(nullCache);
    }

    private String buildCacheKey(String tag, Object... obj) {
        StringBuilder key = new StringBuilder(tag);
        if (obj != null) {
            key.append("|");
            for (int i = 0; i < obj.length; i++) {
                if (i == obj.length - 1) {
                    key.append(obj[i]);
                } else {
                    key.append(obj[i]).append("|");
                }
            }
        }
        return key.toString();
    }

    private static class CacheBean {
        private final long expireTime;
        private final Object value;

        CacheBean(Object value, long expireTime) {
            this.value = value;
            this.expireTime = expireTime;
        }

        Object getValue() {
            return value;
        }

        boolean isExpire() {
            return System.currentTimeMillis() > expireTime;
        }

        boolean notTtl() {
            return expireTime == Long.MAX_VALUE;
        }
    }

    public interface ValueWrapper {

        /**
         * Return the actual value in the cache.
         */
        Object get();
    }

}
