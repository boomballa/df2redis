-- placeholder CAS Lua script for df2redis
-- Camellia proxy should invoke this script to update meta:{key} with timestamp T0
-- KEYS[1] = data key
-- KEYS[2] = meta key
-- ARGV[1] = value (unused here)
-- ARGV[2] = ts
local current = redis.call('GET', KEYS[2])
if current and tonumber(current) and tonumber(current) > tonumber(ARGV[2]) then
    return 0
end
redis.call('SET', KEYS[2], ARGV[2])
return 1
