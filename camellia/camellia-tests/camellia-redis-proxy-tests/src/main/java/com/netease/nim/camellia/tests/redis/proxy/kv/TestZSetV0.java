package com.netease.nim.camellia.tests.redis.proxy.kv;

import com.netease.nim.camellia.core.model.Resource;
import com.netease.nim.camellia.core.util.ResourceTableUtil;
import com.netease.nim.camellia.redis.CamelliaRedisEnv;
import com.netease.nim.camellia.redis.CamelliaRedisTemplate;
import com.netease.nim.camellia.redis.jedis.JedisPoolFactory;
import redis.clients.jedis.JedisPoolConfig;
import redis.clients.jedis.Tuple;

import java.text.SimpleDateFormat;
import java.util.*;
import java.util.concurrent.atomic.AtomicInteger;



public class TestZSetV0 {

    private static final ThreadLocal<SimpleDateFormat> dataFormat = ThreadLocal.withInitial(() -> new SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS"));

    private static final ThreadLocal<AtomicInteger> round = ThreadLocal.withInitial(AtomicInteger::new);

    public static void main(String[] args) throws Exception {
        String url = "redis://pass123@127.0.0.1:6381";
        CamelliaRedisEnv redisEnv = new CamelliaRedisEnv.Builder()
                .jedisPoolFactory(new JedisPoolFactory.DefaultJedisPoolFactory(new JedisPoolConfig(), 6000000))
                .build();
        CamelliaRedisTemplate template = new CamelliaRedisTemplate(redisEnv, ResourceTableUtil.simpleTable(new Resource(url)));

        testZSet(template, true);

        sleep(100);
        System.exit(-1);
    }

    private static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            throw new RuntimeException(e);
        }
    }

    private static final ThreadLocal<String> keyThreadLocal = new ThreadLocal<>();

    public static void testZSet(CamelliaRedisTemplate template, boolean mscore) {

        round.get().incrementAndGet();

        String v1 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();
        String v2 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();
        String v3 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();
        String v4 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();
        String v5 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();
        String v6 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();
        String v7 = UUID.randomUUID().toString() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID() + UUID.randomUUID();

        try {
            String key = UUID.randomUUID().toString().replace("-", "");

            keyThreadLocal.set(key);

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);

                Long zadd1 = template.zadd(key, map1);
                assertEquals(zadd1, 0L);

                map1.put(v7, 7.0);

                Long zadd2 = template.zadd(key, map1);
                assertEquals(zadd2, 1L);

                Set<String> zrange = template.zrange(key, 0, -1);
                assertEquals(zrange.size(), 7);

                Set<String> strings = template.zrangeByScore(key, 0, 100);
                assertEquals(strings.size(), 7);
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);
            }
            {
                Long zcard = template.zcard(key);
                assertEquals(zcard, 6L);
            }
            {
                Map<String, Double> map2 = new HashMap<>();
                map2.put(v6, 6.0);
                map2.put(v7, 7.0);
                Long zadd = template.zadd(key, map2);
                assertEquals(zadd, 1L);
            }
            {
                Long zcard = template.zcard(key);
                assertEquals(zcard, 7L);
            }
            {
                Double v1S = template.zscore(key, v1);
                assertEquals(v1S, 1.0);
            }
            {
                Set<String> set = template.zrange(key, 0, -1);
                assertEquals(set.size(), 7);
            }
            {
                Set<String> set = template.zrange(key, 1, 3);
                assertEquals(set.size(), 3);
                Iterator<String> iterator = set.iterator();
                assertEquals(iterator.next(), v2);
                assertEquals(iterator.next(), v3);
                assertEquals(iterator.next(), v4);
            }
            {
                Set<String> set = template.zrevrange(key, -4, -2);
                assertEquals(set.size(), 3);
                Iterator<String> iterator = set.iterator();
                assertEquals(iterator.next(), v4);
                assertEquals(iterator.next(), v3);
                assertEquals(iterator.next(), v2);
            }
            {
                Set<Tuple> tuples = template.zrangeWithScores(key, 2, 5);
                assertEquals(tuples.size(), 4);
                Iterator<Tuple> iterator1 = tuples.iterator();
                assertEquals(iterator1.next().getElement(), v3);
                assertEquals(iterator1.next().getElement(), v4);
                assertEquals(iterator1.next().getElement(), v5);
                assertEquals(iterator1.next().getElement(), v6);

                Iterator<Tuple> iterator2 = tuples.iterator();
                assertEquals(iterator2.next().getScore(), 3.0);
                assertEquals(iterator2.next().getScore(), 4.0);
                assertEquals(iterator2.next().getScore(), 5.0);
                assertEquals(iterator2.next().getScore(), 6.0);
            }
            {
                Set<Tuple> tuples = template.zrevrangeWithScores(key, 2, 5);
                assertEquals(tuples.size(), 4);
                Iterator<Tuple> iterator1 = tuples.iterator();
                assertEquals(iterator1.next().getElement(), v5);
                assertEquals(iterator1.next().getElement(), v4);
                assertEquals(iterator1.next().getElement(), v3);
                assertEquals(iterator1.next().getElement(), v2);


                Iterator<Tuple> iterator2 = tuples.iterator();
                assertEquals(iterator2.next().getScore(), 5.0);
                assertEquals(iterator2.next().getScore(), 4.0);
                assertEquals(iterator2.next().getScore(), 3.0);
                assertEquals(iterator2.next().getScore(), 2.0);
            }
            {
                Set<String> set = template.zrangeByScore(key, 1.0, 3.0);
                assertEquals(set.size(), 3);
                Iterator<String> iterator = set.iterator();
                assertEquals(iterator.next(), v1);
                assertEquals(iterator.next(), v2);
                assertEquals(iterator.next(), v3);
            }
            {
                Set<String> set = template.zrevrangeByScore(key, 5.0, 3.0);
                assertEquals(set.size(), 3);
                Iterator<String> iterator = set.iterator();
                assertEquals(iterator.next(), v5);
                assertEquals(iterator.next(), v4);
                assertEquals(iterator.next(), v3);
            }
            template.del(key);

            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put("v1", 1.0);
                map1.put("v2", 2.0);
                map1.put("v3", 3.0);
                map1.put("v4", 4.0);
                map1.put("v5", 5.0);
                map1.put("v6", 6.0);
                map1.put("v7", 7.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 7L);
            }

            {
                Set<String> strings = template.zrangeByLex(key, "[v2", "[v5");
                assertEquals(strings.size(), 4);
                Iterator<String> iterator = strings.iterator();
                assertEquals(iterator.next(), "v2");
                assertEquals(iterator.next(), "v3");
                assertEquals(iterator.next(), "v4");
                assertEquals(iterator.next(), "v5");
            }
            {
                Set<String> strings = template.zrangeByLex(key, "-", "+");
                assertEquals(strings.size(), 7);
                Iterator<String> iterator = strings.iterator();
                assertEquals(iterator.next(), "v1");
                assertEquals(iterator.next(), "v2");
                assertEquals(iterator.next(), "v3");
                assertEquals(iterator.next(), "v4");
                assertEquals(iterator.next(), "v5");
                assertEquals(iterator.next(), "v6");
                assertEquals(iterator.next(), "v7");
            }
            {
                Set<String> strings = template.zrevrangeByLex(key, "+", "-");
                assertEquals(strings.size(), 7);
                Iterator<String> iterator = strings.iterator();
                assertEquals(iterator.next(), "v7");
                assertEquals(iterator.next(), "v6");
                assertEquals(iterator.next(), "v5");
                assertEquals(iterator.next(), "v4");
                assertEquals(iterator.next(), "v3");
                assertEquals(iterator.next(), "v2");
                assertEquals(iterator.next(), "v1");
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                map1.put(v7, 6.0);
                template.zadd(key, map1);
            }

            {
                Long zremrange = template.zremrangeByRank(key, 1, 2);
                assertEquals(zremrange, 2L);
            }
            {
                Long zremrange = template.zremrangeByScore(key, 3.0, 5.0);
                assertEquals(zremrange, 2L);
            }
            {
                Long zremrange = template.zremrangeByScore(key, 3.0, 5.0);
                assertEquals(zremrange, 0L);
            }
            {
                Long zrem = template.zrem(key, v1, v6);
                assertEquals(zrem, 2L);
            }
            {
                Long zrem = template.zrem(key, v1, v6);
                assertEquals(zrem, 0L);
            }
            {
                Long zcard = template.zcard(key);
                assertEquals(zcard, 1L);
            }

            template.del(key);

            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put("v1", 1.0);
                map1.put("v2", 2.0);
                map1.put("v3", 3.0);
                map1.put("v4", 4.0);
                map1.put("v5", 5.0);
                map1.put("v6", 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);

                Set<String> strings = template.zrangeByLex(key, "[v1", "(v3");
                assertEquals(strings.size(), 2);
                Iterator<String> iterator = strings.iterator();
                assertEquals(iterator.next(), "v1");
                assertEquals(iterator.next(), "v2");

                Long zremrange = template.zremrangeByLex(key, "(v1", "(v3");
                assertEquals(zremrange, 1L);
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                template.zadd(key, map1);
            }

            {
                Set<String> set = template.zrange(key, 0, -1);
                assertEquals(set.size(), 5);
                Iterator<String> iterator = set.iterator();
                assertEquals(iterator.next(), v1);
                assertEquals(iterator.next(), v3);
                assertEquals(iterator.next(), v4);
                assertEquals(iterator.next(), v5);
                assertEquals(iterator.next(), v6);
            }
            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);
                Set<String> strings = template.zrangeByScore(key, "3.0", "5.0");
                assertEquals(strings.size(), 3);
                Iterator<String> iterator = strings.iterator();
                assertEquals(iterator.next(), v3);
                assertEquals(iterator.next(), v4);
                assertEquals(iterator.next(), v5);

                Set<String> strings1 = template.zrangeByScore(key, "(3.0", "5.0");
                assertEquals(strings1.size(), 2);
                Iterator<String> iterator1 = strings1.iterator();
                assertEquals(iterator1.next(), v4);
                assertEquals(iterator1.next(), v5);

                Set<String> strings2 = template.zrangeByScore(key, "(3.0", "+inf");
                assertEquals(strings2.size(), 3);
                Iterator<String> iterator2 = strings2.iterator();
                assertEquals(iterator2.next(), v4);
                assertEquals(iterator2.next(), v5);
                assertEquals(iterator2.next(), v6);
            }
            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);
                Long zcount = template.zcount(key, 1, 3);
                assertEquals(zcount, 3L);
                template.zremrangeByScore(key, 1, 3);
                Long zcount1 = template.zcount(key, 2, 5);
                assertEquals(zcount1, 2L);
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put("a", 1.0);
                map1.put("b", 2.0);
                map1.put("c", 3.0);
                map1.put("d", 4.0);
                map1.put("e", 5.0);
                map1.put("f", 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);
                Long zcount = template.zlexcount(key, "(a", "[d");
                assertEquals(zcount, 3L);
                template.zremrangeByScore(key, 1, 3);
                Long zcount1 = template.zlexcount(key, "-", "(e");
                assertEquals(zcount1, 1L);
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);
                Double zscore = template.zscore(key, "g");
                assertEquals(zscore, null);

                if (mscore) {
                    List<Double> list = template.zmscore(key, v1, v2, v3, "h", v4);
                    assertEquals(list.size(), 5);
                    assertEquals(list.get(0), 1.0);
                    assertEquals(list.get(1), 2.0);
                    assertEquals(list.get(2), 3.0);
                    assertEquals(list.get(3), null);
                    assertEquals(list.get(4), 4.0);
                }
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                map1.put(v4, 4.0);
                map1.put(v5, 5.0);
                map1.put(v6, 6.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 6L);

                {
                    Long a = template.zrank(key, v1);
                    assertEquals(a, 0L);

                    Long c = template.zrank(key, v3);
                    assertEquals(c, 2L);

                    Long g = template.zrank(key, "g");
                    assertEquals(g, null);
                }

                {
                    Long a = template.zrevrank(key, v1);
                    assertEquals(a, 5L);

                    Long c = template.zrevrank(key, v3);
                    assertEquals(c, 3L);

                    Long g = template.zrevrank(key, "g");
                    assertEquals(g, null);
                }
            }

            template.del(key);
            {
                Map<String, Double> map1 = new HashMap<>();
                map1.put(v1, 1.0);
                map1.put(v2, 2.0);
                map1.put(v3, 3.0);
                Long zadd = template.zadd(key, map1);
                assertEquals(zadd, 3L);

                Set<String> zrange = template.zrange(key, 0, -1);
                assertEquals(zrange.size(), 3);

                template.zrem(key, v1, v2);
                template.zrem(key, v3);

                String type = template.type(key);
                assertEquals(type, "none");

                String v = template.setex(key, 10, "v");
                assertEquals(v, "OK");
            }

            template.del(key);
            {
                for (int count = 99; count < 202; count ++) {
                    template.del(key);

                    List<Map<String, Double>> list = new ArrayList<>();
                    Map<String, Double> map = new HashMap<>();
                    for (int i = 0; i < count; i++) {
                        map.put("m-" + i, (double) (i + 1000));
                        if (map.size() >= 50) {
                            list.add(map);
                            map = new HashMap<>();
                        }
                    }
                    if (!map.isEmpty()) {
                        list.add(map);
                    }
                    for (Map<String, Double> doubleMap : list) {
                        template.zadd(key, doubleMap);
                    }


                    Set<String> zrange = template.zrange(key, 0, -1);
                    assertEquals(zrange.size(), count);


                    Set<String> strings = template.zrangeByScore(key, 0, System.currentTimeMillis() + 1000);
                    assertEquals(strings.size(), count);

                    Set<String> strings1 = template.zrangeByScore(key, 0, System.currentTimeMillis() + 1000, 0, 1);
                    assertEquals(strings1.size(), 1);
                    assertEquals(strings1.iterator().next(), "m-0");

                    Set<String> zrevrange = template.zrevrange(key, 0, -1);
                    assertEquals(zrevrange.size(), count);

                    Set<String> strings2 = template.zrevrangeByScore(key, System.currentTimeMillis() + 1000, 0);
                    assertEquals(strings2.size(), count);

                    Set<String> strings3 = template.zrevrangeByScore(key, System.currentTimeMillis() + 1000, 0, 0, 1);
                    assertEquals(strings3.size(), 1);
                    assertEquals(strings3.iterator().next(), "m-" + (count - 1));

                    Set<String> strings4 = template.zrangeByLex(key, "-", "+");
                    assertEquals(strings4.size(), count);

                    Set<String> strings6 = template.zrangeByLex(key, "-", "+", 0, 1);
                    assertEquals(strings6.size(), 1);

                    Set<String> strings5 = template.zrevrangeByLex(key, "+", "-");
                    assertEquals(strings5.size(), count);

                    Set<String> strings7 = template.zrevrangeByLex(key, "+", "-", 0, 1);
                    assertEquals(strings7.size(), 1);
                }
            }


            template.del(key);
        } catch (Exception e) {
            System.out.println("error");
            e.printStackTrace();
            sleep(100);
            System.exit(-1);
        }

    }

    private static void assertEquals(Object result, Object expect) {
        if (Objects.equals(result, expect)) {
            System.out.println("zsetv0, round=" + round.get().get() + ", SUCCESS, thread=" + Thread.currentThread().getName()
                    + ", key = " + keyThreadLocal.get() + ", time = " + dataFormat.get().format(new Date()));
        } else {
            System.out.println("zsetv0, round=" + round.get().get() + ", ERROR, expect " + expect + " but found " + result + "," +
                    " thread=" + Thread.currentThread().getName() + ", key = " + keyThreadLocal.get() + ", time = " + dataFormat.get().format(new Date()));
            throw new RuntimeException();
        }
    }
}
