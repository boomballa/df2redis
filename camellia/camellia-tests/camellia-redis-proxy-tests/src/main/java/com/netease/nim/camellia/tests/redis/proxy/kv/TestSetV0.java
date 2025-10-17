package com.netease.nim.camellia.tests.redis.proxy.kv;

import com.netease.nim.camellia.core.model.Resource;
import com.netease.nim.camellia.core.util.ResourceTableUtil;
import com.netease.nim.camellia.redis.CamelliaRedisEnv;
import com.netease.nim.camellia.redis.CamelliaRedisTemplate;
import com.netease.nim.camellia.redis.jedis.JedisPoolFactory;
import redis.clients.jedis.JedisPoolConfig;

import java.text.SimpleDateFormat;
import java.util.*;
import java.util.concurrent.atomic.AtomicInteger;


public class TestSetV0 {
    private static final ThreadLocal<SimpleDateFormat> dataFormat = ThreadLocal.withInitial(() -> new SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS"));

    private static final ThreadLocal<AtomicInteger> round = ThreadLocal.withInitial(AtomicInteger::new);

    public static void main(String[] args) {
        String url = "redis://pass123@127.0.0.1:6381";
        CamelliaRedisEnv redisEnv = new CamelliaRedisEnv.Builder()
                .jedisPoolFactory(new JedisPoolFactory.DefaultJedisPoolFactory(new JedisPoolConfig(), 6000000))
                .build();
        CamelliaRedisTemplate template = new CamelliaRedisTemplate(redisEnv, ResourceTableUtil.simpleTable(new Resource(url)));

        testSet(template);

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

    public static void testSet(CamelliaRedisTemplate template) {

        round.get().incrementAndGet();

        try {
            String key = UUID.randomUUID().toString().replace("-", "");
            keyThreadLocal.set(key);
            //
            template.del(key);
            {
                Long sadd = template.sadd(key, "a", "b", "c", "d", "e", "f");
                assertEquals(sadd, 6L);

                Long sadd1 = template.sadd(key, "a", "b", "c", "d", "e", "f", "g");
                assertEquals(sadd1, 1L);

                Long sadd2 = template.sadd(key, "a", "b");
                assertEquals(sadd2, 0L);

                Set<String> smembers = template.smembers(key);
                assertEquals(smembers.size(), 7);
            }

            template.del(key);
            {
                Long sadd = template.sadd(key, "a", "b", "c", "d", "e", "f");
                assertEquals(sadd, 6L);

                Long sadd1 = template.sadd(key, "a", "b", "c", "d", "e", "f", "g");
                assertEquals(sadd1, 1L);

                Set<String> smembers = template.smembers(key);
                assertEquals(smembers.size(), 7);
                assertEquals(smembers.contains("a"), true);
                assertEquals(smembers.contains("z"), false);

                Boolean a = template.sismember(key, "a");
                assertEquals(a, true);
                Boolean z = template.sismember(key, "z");
                assertEquals(z, false);

                List<Boolean> list = template.smismember(key, "a", "z");
                assertEquals(list.size(), 2);
                assertEquals(list.get(0), true);
                assertEquals(list.get(1), false);

                String srandmember = template.srandmember(key);
                assertEquals(srandmember != null, true);

                List<String> srandmember1 = template.srandmember(key, 2);
                assertEquals(srandmember1.size(), 2);

                Long scard = template.scard(key);
                assertEquals(scard, 7L);

                String spop = template.spop(key);
                assertEquals(spop != null, true);

                Set<String> spop1 = template.spop(key, 2);
                assertEquals(spop1.size(), 2);

                Boolean sismember = template.sismember(key, spop);
                assertEquals(sismember, false);

                for (String s : spop1) {
                    Boolean sismember1 = template.sismember(key, s);
                    assertEquals(sismember1, false);
                }

                Long scard1 = template.scard(key);
                assertEquals(scard1, 4L);

                Set<String> smembers1 = template.smembers(key);

                Long srem = template.srem(key, smembers1.iterator().next());
                assertEquals(srem, 1L);

                Set<String> smembers2 = template.smembers(key);
                Iterator<String> iterator = smembers2.iterator();
                String next = iterator.next();
                String next1 = iterator.next();
                Long srem1 = template.srem(key, next, next1);
                assertEquals(srem1, 2L);

                Long scard2 = template.scard(key);
                assertEquals(scard2, 1L);

                Set<String> smembers3 = template.smembers(key);
                template.srem(key, smembers3.toArray(new String[0]));

                String a1 = template.setex(key, 10, "A");
                assertEquals(a1, "OK");

                template.del(key);

                template.sadd(key, "A", "b");
                template.expire(key, 4);
                Set<String> smembers4 = template.smembers(key);
                assertEquals(smembers4.size(), 2);
                Thread.sleep(4050);
                Set<String> smembers5 = template.smembers(key);
                assertEquals(smembers5.size(), 0);
            }

            template.del(key);
            {
                Long sadd = template.sadd(key, "a", "b", "c");
                assertEquals(sadd, 3L);

                Set<String> smembers = template.smembers(key);
                assertEquals(smembers.size(), 3);

                template.srem(key, "a", "b");
                template.srem(key, "c");

                String type = template.type(key);
                assertEquals(type, "none");

                String v = template.setex(key, 10, "v");
                assertEquals(v, "OK");
            }

            template.del(key);
            {
                for (int i=90; i<210; i++) {
                    template.del(key);
                    List<Set<String>> list = new ArrayList<>();
                    Set<String> set = new HashSet<>();
                    for (int j=0; j<i; j++) {
                        set.add("a" + j);
                        if (set.size() >= 50) {
                            list.add(set);
                            set = new HashSet<>();
                        }
                    }
                    if (!set.isEmpty()) {
                        list.add(set);
                    }
                    for (Set<String> strings : list) {
                        template.sadd(key, strings.toArray(new String[0]));
                    }
                    Set<String> smembers = template.smembers(key);
                    assertEquals(smembers.size(), i);

                    Long scard = template.scard(key);
                    assertEquals(scard, (long) i);
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
            System.out.println("setv0, round=" + round.get().get() + ", SUCCESS, thread=" + Thread.currentThread().getName()
                    + ", key = " + keyThreadLocal.get() + ", time = " + dataFormat.get().format(new Date()));
        } else {
            System.out.println("setv0, round=" + round.get().get() + ", ERROR, expect " + expect + " but found " + result + "," +
                    " thread=" + Thread.currentThread().getName() + ", key = " + keyThreadLocal.get() + ", time = " + dataFormat.get().format(new Date()));
            throw new RuntimeException();
        }
    }
}
