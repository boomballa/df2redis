-- 默认 meta CAS 脚本模板，如未在配置中指定将由 df2redis 自动复制
local current = redis.call('GET', KEYS[2])
if current and tonumber(current) and tonumber(current) > tonumber(ARGV[2]) then
    return 0
end
redis.call('SET', KEYS[2], ARGV[2])
return 1
