package stores

// CacheLen exposes the leaderboard cache size to external tests.
func (s *RankingStore) CacheLen() int {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return len(s.cache)
}
