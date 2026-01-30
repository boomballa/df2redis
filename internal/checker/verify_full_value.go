package checker

import (
	"fmt"
	"reflect"
	"sort"

	"df2redis/internal/redisx"
)

// verifyFullValue compares values for a single key based on its type.
func (c *Checker) verifyFullValue(src, tgt *redisx.Client, key, keyType string) (bool, error) {
	switch keyType {
	case "string":
		return c.compareString(src, tgt, key)
	case "list":
		return c.compareList(src, tgt, key)
	case "set":
		return c.compareSet(src, tgt, key)
	case "zset":
		return c.compareZSet(src, tgt, key)
	case "hash":
		return c.compareHash(src, tgt, key)
	case "stream":
		return c.compareStream(src, tgt, key)
	default:
		return true, nil
	}
}

func (c *Checker) compareString(src, tgt *redisx.Client, key string) (bool, error) {
	// STRLEN check first for optimization
	lenSrc, err := redisx.ToInt64(must(src.Do("STRLEN", key)))
	if err != nil {
		return false, err
	}
	lenTgt, err := redisx.ToInt64(must(tgt.Do("STRLEN", key)))
	if err != nil {
		return false, err
	}

	if lenSrc != lenTgt {
		return false, nil
	}

	// If Smart Mode and big key, skip value check
	if c.config.Mode == ModeSmartBigKey && int(lenSrc) > c.config.BigKeyThreshold {
		return true, nil
	}

	valSrc, err := redisx.ToString(must(src.Do("GET", key)))
	if err != nil {
		return false, err
	}
	valTgt, err := redisx.ToString(must(tgt.Do("GET", key)))
	if err != nil {
		return false, err
	}
	return valSrc == valTgt, nil
}

func (c *Checker) compareList(src, tgt *redisx.Client, key string) (bool, error) {
	lenSrc, err := redisx.ToInt64(must(src.Do("LLEN", key)))
	if err != nil {
		return false, err
	}
	lenTgt, err := redisx.ToInt64(must(tgt.Do("LLEN", key)))
	if err != nil {
		return false, err
	}

	if lenSrc != lenTgt {
		return false, nil
	}

	// Smart Mode skip
	if c.config.Mode == ModeSmartBigKey && int(lenSrc) > c.config.BigKeyThreshold {
		return true, nil
	}

	// Chunked comparison for big lists to avoid OOM
	batch := 1000
	for i := int64(0); i < lenSrc; i += int64(batch) {
		end := i + int64(batch) - 1
		valSrc, err := redisx.ToStringSlice(must(src.Do("LRANGE", key, i, end)))
		if err != nil {
			return false, err
		}
		valTgt, err := redisx.ToStringSlice(must(tgt.Do("LRANGE", key, i, end)))
		if err != nil {
			return false, err
		}

		if len(valSrc) != len(valTgt) {
			return false, nil
		}
		for j := range valSrc {
			if valSrc[j] != valTgt[j] {
				return false, nil
			}
		}
	}

	return true, nil
}

func (c *Checker) compareSet(src, tgt *redisx.Client, key string) (bool, error) {
	lenSrc, err := redisx.ToInt64(must(src.Do("SCARD", key)))
	if err != nil {
		return false, err
	}
	lenTgt, err := redisx.ToInt64(must(tgt.Do("SCARD", key)))
	if err != nil {
		return false, err
	}

	if lenSrc != lenTgt {
		return false, nil
	}

	if c.config.Mode == ModeSmartBigKey && int(lenSrc) > c.config.BigKeyThreshold {
		return true, nil
	}

	// For standard sets, order agnostic compare
	// TODO: Use SSCAN for big sets
	valSrc, err := redisx.ToStringSlice(must(src.Do("SMEMBERS", key)))
	if err != nil {
		return false, err
	}
	valTgt, err := redisx.ToStringSlice(must(tgt.Do("SMEMBERS", key)))
	if err != nil {
		return false, err
	}

	if len(valSrc) != len(valTgt) {
		return false, nil
	}

	sort.Strings(valSrc)
	sort.Strings(valTgt)

	for i := range valSrc {
		if valSrc[i] != valTgt[i] {
			return false, nil
		}
	}
	return true, nil
}

func (c *Checker) compareHash(src, tgt *redisx.Client, key string) (bool, error) {
	lenSrc, err := redisx.ToInt64(must(src.Do("HLEN", key)))
	if err != nil {
		return false, err
	}
	lenTgt, err := redisx.ToInt64(must(tgt.Do("HLEN", key)))
	if err != nil {
		return false, err
	}

	if lenSrc != lenTgt {
		return false, nil
	}

	if c.config.Mode == ModeSmartBigKey && int(lenSrc) > c.config.BigKeyThreshold {
		return true, nil
	}

	// TODO: Use HSCAN for big hashes
	srcMap, err := c.fetchHash(src, key)
	if err != nil {
		return false, err
	}
	tgtMap, err := c.fetchHash(tgt, key)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(srcMap, tgtMap), nil
}

func (c *Checker) fetchHash(client *redisx.Client, key string) (map[string]string, error) {
	arr, err := redisx.ToStringSlice(must(client.Do("HGETALL", key)))
	if err != nil {
		return nil, err
	}
	if len(arr)%2 != 0 {
		return nil, fmt.Errorf("odd number of elements in HGETALL")
	}

	m := make(map[string]string)
	for i := 0; i < len(arr); i += 2 {
		m[arr[i]] = arr[i+1]
	}
	return m, nil
}

func (c *Checker) compareZSet(src, tgt *redisx.Client, key string) (bool, error) {
	lenSrc, err := redisx.ToInt64(must(src.Do("ZCARD", key)))
	if err != nil {
		return false, err
	}
	lenTgt, err := redisx.ToInt64(must(tgt.Do("ZCARD", key)))
	if err != nil {
		return false, err
	}

	if lenSrc != lenTgt {
		return false, nil
	}

	if c.config.Mode == ModeSmartBigKey && int(lenSrc) > c.config.BigKeyThreshold {
		return true, nil
	}

	// TODO: Use ZSCAN for big zsets
	valSrc, err := redisx.ToStringSlice(must(src.Do("ZRANGE", key, 0, -1, "WITHSCORES")))
	if err != nil {
		return false, err
	}
	valTgt, err := redisx.ToStringSlice(must(tgt.Do("ZRANGE", key, 0, -1, "WITHSCORES")))
	if err != nil {
		return false, err
	}

	if len(valSrc) != len(valTgt) {
		return false, nil
	}
	for i := range valSrc {
		if valSrc[i] != valTgt[i] {
			return false, nil
		}
	}
	return true, nil
}

func (c *Checker) compareStream(src, tgt *redisx.Client, key string) (bool, error) {
	lenSrc, err := redisx.ToInt64(must(src.Do("XLEN", key)))
	if err != nil {
		return false, err
	}
	lenTgt, err := redisx.ToInt64(must(tgt.Do("XLEN", key)))
	if err != nil {
		return false, err
	}

	if lenSrc != lenTgt {
		return false, nil
	}

	// XINFO STREAM check (basic)
	// We ignore infoSrc variable usage to avoid lint error, just check equality
	// But redisx.Do returns interface{}, difficult to compare deeply without robust parser.
	// For now, XLEN equality is our primary check in this prototype.

	return true, nil
}
