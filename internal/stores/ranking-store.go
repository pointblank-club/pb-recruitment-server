package stores

import (
	"app/internal/common"
	"app/internal/models"
	"app/internal/models/dto"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

type leaderboardCacheEntry struct {
	data      *dto.GetLeaderboardResponse
	expiresAt time.Time
}

type RankingStore struct {
	db       *sql.DB
	cache    map[string]leaderboardCacheEntry
	cacheMu  sync.RWMutex
	cacheTTL time.Duration
}

func NewRankingStore(db *sql.DB) *RankingStore {
	ttl := 3 * time.Second
	if ttlStr := os.Getenv("LEADERBOARD_CACHE_TTL_SECONDS"); ttlStr != "" {
		if sec, err := strconv.Atoi(ttlStr); err == nil && sec >= 0 {
			if sec > 60 {
				sec = 60
			}
			ttl = time.Duration(sec) * time.Second
		}
	}
	return &RankingStore{
		db:       db,
		cache:    make(map[string]leaderboardCacheEntry),
		cacheTTL: ttl,
	}
}

func (s *RankingStore) SetCacheTTL(d time.Duration) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cacheTTL = d
}

func (s *RankingStore) InvalidateCache(contestID string) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if contestID == "" {
		s.cache = make(map[string]leaderboardCacheEntry)
		return
	}
	prefix := contestID + ":"
	for k := range s.cache {
		if strings.HasPrefix(k, prefix) {
			delete(s.cache, k)
		}
	}
}

func cloneLeaderboardResponse(res *dto.GetLeaderboardResponse) *dto.GetLeaderboardResponse {
	if res == nil {
		return nil
	}
	clone := *res
	clone.Entries = make([]dto.LeaderboardEntry, len(res.Entries))
	for i, e := range res.Entries {
		entryCopy := e
		if e.ProblemScores != nil {
			entryCopy.ProblemScores = make([]dto.ProblemScore, len(e.ProblemScores))
			copy(entryCopy.ProblemScores, e.ProblemScores)
		}
		clone.Entries[i] = entryCopy
	}
	return &clone
}

func (s *RankingStore) UpdateLeaderboardUser(ctx context.Context, contestID string, userID string, req *dto.UpdateLeaderboardUserRequest) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ranking store: db is not initialized")
	}

	if req.Hidden == nil && req.Disqualified == nil {
		return fmt.Errorf("no fields to update")
	}

	// Verify participant existence in this contest (either existing ranking or contest registration)
	var exists bool
	checkQuery := `
		SELECT EXISTS (
			SELECT 1 FROM rankings WHERE contest_id = $1 AND user_id = $2
			UNION
			SELECT 1 FROM contest_registrations cr
			JOIN users u ON cr.user_id = u.id
			WHERE cr.contest_id = $1 AND cr.user_id = $2
		)
	`
	err := s.db.QueryRowContext(ctx, checkQuery, contestID, userID).Scan(&exists)
	if err != nil {
		log.Printf("ranking-store: check participant failed: %v", err)
		return fmt.Errorf("check participant: %w", err)
	}
	if !exists {
		return common.UserNotRegisteredError
	}

	query := `
		INSERT INTO rankings (contest_id, user_id, score, rank, hidden, disqualified, shortlisted)
		VALUES ($1, $2, 0, 0, COALESCE($3, false), COALESCE($4, false), false)
		ON CONFLICT (contest_id, user_id)
		DO UPDATE SET
			hidden = COALESCE($3, rankings.hidden),
			disqualified = COALESCE($4, rankings.disqualified)
	`

	_, err = s.db.ExecContext(ctx, query, contestID, userID, req.Hidden, req.Disqualified)
	if err != nil {
		log.Printf("ranking-store: update failed: %v", err)
		return fmt.Errorf("update ranking: %w", err)
	}

	s.InvalidateCache(contestID)

	return nil
}

const leaderboardPageSize = 20

func (s *RankingStore) GetLeaderboard(ctx context.Context, contestID string, page int) (*dto.GetLeaderboardResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("ranking store: not initialized")
	}

	page = max(0, page)
	cacheKey := fmt.Sprintf("%s:%d", contestID, page)

	if s.cacheTTL > 0 {
		s.cacheMu.RLock()
		entry, ok := s.cache[cacheKey]
		s.cacheMu.RUnlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return cloneLeaderboardResponse(entry.data), nil
		}
	}

	if s.db == nil {
		return nil, fmt.Errorf("ranking store: db is not initialized")
	}

	const checkContestQ = `SELECT EXISTS(SELECT 1 FROM contests WHERE id = $1)`
	var exists bool
	if err := s.db.QueryRowContext(ctx, checkContestQ, contestID).Scan(&exists); err != nil {
		log.Printf("ranking-store: check contest query failed: %v", err)
		return nil, fmt.Errorf("check contest: %w", err)
	}
	if !exists {
		return nil, common.ContestNotFoundError
	}

	offset := page * leaderboardPageSize

	const countQ = `
		SELECT COUNT(*)
		FROM rankings r
		INNER JOIN users u ON r.user_id = u.id
		WHERE r.contest_id = $1
		  AND r.hidden = false
		  AND r.disqualified = false
	`
	var totalCount int
	err := s.db.QueryRowContext(ctx, countQ, contestID).Scan(&totalCount)
	if err != nil {
		log.Printf("ranking-store: count query failed: %v", err)
		return nil, fmt.Errorf("count rankings: %w", err)
	}

	totalPages := (totalCount + leaderboardPageSize - 1) / leaderboardPageSize

	var isConsolidated bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rankings WHERE contest_id = $1 AND rank > 0)`, contestID).Scan(&isConsolidated); err != nil {
		log.Printf("ranking-store: check consolidated query failed: %v", err)
		return nil, fmt.Errorf("check consolidated rankings: %w", err)
	}

	entriesQ := `
		SELECT RANK() OVER (ORDER BY r.score DESC) AS rank, r.user_id, u.name, u.usn, r.score
		FROM rankings r
		INNER JOIN users u 
		ON r.user_id = u.id
		WHERE r.contest_id = $1
		AND r.hidden = false
		AND r.disqualified = false
		ORDER BY rank ASC, u.name ASC, r.user_id ASC
		LIMIT $2 OFFSET $3
	`
	if isConsolidated {
		entriesQ = `
		SELECT RANK() OVER (ORDER BY r.rank = 0, r.rank) AS rank, r.user_id, u.name, u.usn, r.score
		FROM rankings r
		INNER JOIN users u 
		ON r.user_id = u.id
		WHERE r.contest_id = $1
		AND r.hidden = false
		AND r.disqualified = false
		ORDER BY 1 ASC, r.user_id ASC
		LIMIT $2 OFFSET $3
	`
	}

	rows, err := s.db.QueryContext(ctx, entriesQ, contestID, leaderboardPageSize, offset)
	if err != nil {
		log.Printf("ranking-store: leaderboard query failed: %v", err)
		return nil, fmt.Errorf("query leaderboard: %w", err)
	}
	defer rows.Close()

	entries := make([]dto.LeaderboardEntry, 0, leaderboardPageSize)
	userIDs := make([]string, 0, leaderboardPageSize)

	for rows.Next() {
		var e dto.LeaderboardEntry
		if err := rows.Scan(&e.Rank, &e.UserID, &e.Username, &e.USN, &e.TotalScore); err != nil {
			log.Printf("ranking-store: row scan failed: %v", err)
			return nil, fmt.Errorf("scan leaderboard row: %w", err)
		}
		entries = append(entries, e)
		userIDs = append(userIDs, e.UserID)
	}
	if err := rows.Err(); err != nil {
		log.Printf("ranking-store: rows error: %v", err)
		return nil, fmt.Errorf("rows error: %w", err)
	}

	problemScores, err := s.getProblemScores(ctx, contestID, userIDs)
	if err != nil {
		log.Printf("ranking-store: problem scores query failed : %v", err)
		return nil, err
	}

	for i := range entries {
		scores, ok := problemScores[entries[i].UserID]
		if !ok {
			continue
		}
		entries[i].ProblemScores = scores
		entries[i].ProblemsSolved, entries[i].LastSubmissionTime = summarizeSolves(scores)
	}

	resp := &dto.GetLeaderboardResponse{
		Entries:    entries,
		Page:       page,
		TotalPages: totalPages,
		TotalCount: totalCount,
	}

	if s.cacheTTL > 0 && page < max(totalPages, 1) {
		s.cacheMu.Lock()
		s.cache[cacheKey] = leaderboardCacheEntry{
			data:      resp,
			expiresAt: time.Now().Add(s.cacheTTL),
		}
		s.cacheMu.Unlock()
	}

	return cloneLeaderboardResponse(resp), nil
}

// summarizeSolves reports how many problems the user solved and the time at
// which they reached their final score: the latest of their per-problem first
// solves. Resubmitting an already-solved problem must not push that later.
// A problem counts as solved only when it has an accepted submission, so a
// zero-point problem is not mistaken for one. lastSolve is 0 when nothing was
// solved.
func summarizeSolves(scores []dto.ProblemScore) (solved int, lastSolve int64) {
	for _, ps := range scores {
		if ps.SolvedAt == nil {
			continue
		}
		solved++
		if *ps.SolvedAt > lastSolve {
			lastSolve = *ps.SolvedAt
		}
	}
	return solved, lastSolve
}

// Attempts counts all submissions for display; the final-ranking penalty excludes judge errors.
func (s *RankingStore) getProblemScores(ctx context.Context, contestID string, userIDs []string) (map[string][]dto.ProblemScore, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(userIDs))
	args := make([]interface{}, 0, len(userIDs)+1)
	args = append(args, contestID)
	for i, uid := range userIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, uid)
	}

	query := fmt.Sprintf(`
		SELECT
			sub.user_id,
			sub.problem_id,
			p.name,
			p.score AS max_score,
			COUNT(sub.id) AS attempts,
			MAX(CASE WHEN sub.status = 'accepted' THEN p.score ELSE 0 END) AS score,
			MIN(CASE WHEN sub.status = 'accepted' THEN sub.created_at ELSE NULL END) AS solved_at
		FROM submissions sub
		INNER JOIN problems p ON sub.problem_id = p.id AND sub.contest_id = p.contest_id
		WHERE sub.contest_id = $1
		  AND sub.user_id IN (%s)
		GROUP BY sub.user_id, sub.problem_id, p.name, p.score
		ORDER BY sub.user_id, p.name
	`, strings.Join(placeholders, ", "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query problem scores: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]dto.ProblemScore, len(userIDs))
	for rows.Next() {
		var userID string
		var ps dto.ProblemScore
		var solvedAt sql.NullInt64

		if err := rows.Scan(
			&userID,
			&ps.ProblemID,
			&ps.ProblemName,
			&ps.MaxScore,
			&ps.Attempts,
			&ps.Score,
			&solvedAt,
		); err != nil {
			log.Printf("ranking-store: problem score row scan failed: %v", err)
			return nil, fmt.Errorf("scan problem score row: %w", err)
		}

		if solvedAt.Valid {
			ts := solvedAt.Int64 * 1000
			ps.SolvedAt = &ts
		}

		result[userID] = append(result[userID], ps)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("problem scores rows error: %w", err)
	}

	return result, nil
}

type participantSummary struct {
	UserID             string
	TotalScore         int
	ProblemsSolved     int
	LastSubmissionTime int64
	TotalAttempts      int // Ranking penalty, excluding judge errors.
	Rank               int
	Hidden             bool
	Disqualified       bool
	Shortlisted        bool
}

func isTied(a, b participantSummary) bool {
	return a.TotalScore == b.TotalScore &&
		a.ProblemsSolved == b.ProblemsSolved &&
		a.LastSubmissionTime == b.LastSubmissionTime &&
		a.TotalAttempts == b.TotalAttempts
}

func SortParticipantsForRanking(p []participantSummary) {
	sort.SliceStable(p, func(i, j int) bool {
		a, b := p[i], p[j]
		if a.Disqualified != b.Disqualified {
			return !a.Disqualified && b.Disqualified
		}
		if a.TotalScore != b.TotalScore {
			return a.TotalScore > b.TotalScore
		}
		if a.ProblemsSolved != b.ProblemsSolved {
			return a.ProblemsSolved > b.ProblemsSolved
		}
		if a.LastSubmissionTime != b.LastSubmissionTime {
			return a.LastSubmissionTime < b.LastSubmissionTime
		}
		if a.TotalAttempts != b.TotalAttempts {
			return a.TotalAttempts < b.TotalAttempts
		}
		return a.UserID < b.UserID
	})
}

func AssignRanks(p []participantSummary) {
	for i := range p {
		if p[i].Disqualified {
			p[i].Rank = 0
			continue
		}
		if i > 0 && !p[i-1].Disqualified && isTied(p[i], p[i-1]) {
			p[i].Rank = p[i-1].Rank
		} else {
			p[i].Rank = i + 1
		}
	}
}

func (s *RankingStore) ConsolidateContest(ctx context.Context, contestID string, regradeMCQs bool, force bool) (*dto.ConsolidationResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("ranking store: db is not initialized")
	}

	const checkContestQ = `SELECT EXISTS(SELECT 1 FROM contests WHERE id = $1)`
	var exists bool
	if err := s.db.QueryRowContext(ctx, checkContestQ, contestID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check contest: %w", err)
	}
	if !exists {
		return nil, common.ContestNotFoundError
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin consolidation transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Advisory transaction lock per contest to serialize concurrent consolidations.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, contestID); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}

	// 2. Grade MCQs FIRST (locks submissions before rankings, matching JudgeMCQ/ProcessFinal order).
	mcqsGraded, err := s.gradeContestMCQsTx(ctx, tx, contestID, regradeMCQs)
	if err != nil {
		return nil, fmt.Errorf("grade MCQs: %w", err)
	}

	// 3. Ensure ranking rows exist for all registered participants and users with submissions.
	const ensureRankingsQ = `
		INSERT INTO rankings (contest_id, user_id)
		SELECT $1, u.user_id FROM (
			SELECT user_id FROM contest_registrations WHERE contest_id = $1
			UNION
			SELECT user_id FROM submissions WHERE contest_id = $1
		) u
		ON CONFLICT (contest_id, user_id) DO NOTHING
	`
	if _, err := tx.ExecContext(ctx, ensureRankingsQ, contestID); err != nil {
		return nil, fmt.Errorf("ensure ranking rows: %w", err)
	}

	// 4. Lock rankings rows in deterministic user_id order.
	if _, err := tx.ExecContext(ctx, `SELECT user_id FROM rankings WHERE contest_id = $1 ORDER BY user_id FOR UPDATE`, contestID); err != nil {
		return nil, fmt.Errorf("lock rankings: %w", err)
	}

	// 5. Count pending code submissions.
	var pendingCodeSubmissions int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE contest_id = $1 AND type = 'code' AND status = 'pending'`, contestID).Scan(&pendingCodeSubmissions); err != nil {
		return nil, fmt.Errorf("count pending code submissions: %w", err)
	}

	if !force && pendingCodeSubmissions > 0 {
		return nil, common.ErrPendingSubmissionsExist
	}

	// 6. Compute and save ranks within the transaction via set-based upsert.
	rankedCount, err := s.computeAndSaveRanksTx(ctx, tx, contestID)
	if err != nil {
		return nil, fmt.Errorf("compute and save ranks: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit consolidation: %w", err)
	}

	s.InvalidateCache(contestID)

	return &dto.ConsolidationResponse{
		ContestID:          contestID,
		MCQsGraded:         mcqsGraded,
		ParticipantsRanked: rankedCount,
		PendingSubmissions: pendingCodeSubmissions,
		ProcessedAt:        time.Now().Unix(),
		Status:             "success",
	}, nil
}

func (s *RankingStore) gradeContestMCQsTx(ctx context.Context, tx *sql.Tx, contestID string, regrade bool) (int, error) {
	query := `
		SELECT sub.id, sub.choices, p.answer
		FROM submissions sub
		INNER JOIN problems p ON p.id = sub.problem_id AND p.contest_id = sub.contest_id
		WHERE sub.contest_id = $1 AND sub.type = 'mcq' AND p.type = 'mcq'
	`
	if !regrade {
		query += ` AND sub.status = 'pending'`
	}
	query += ` ORDER BY sub.id ASC FOR UPDATE OF sub`

	rows, err := tx.QueryContext(ctx, query, contestID)
	if err != nil {
		return 0, fmt.Errorf("query MCQ submissions: %w", err)
	}
	defer rows.Close()

	type mcqGradingResult struct {
		id     string
		status models.SubmissionStatus
	}

	var results []mcqGradingResult
	for rows.Next() {
		var id string
		var choices, answer pq.Int64Array
		if err := rows.Scan(&id, &choices, &answer); err != nil {
			return 0, fmt.Errorf("scan MCQ submission: %w", err)
		}
		status := models.WrongAnswer
		if mcqAnswersMatch(choices, answer) {
			status = models.Accepted
		}
		results = append(results, mcqGradingResult{id: id, status: status})
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("MCQ iteration error: %w", err)
	}

	if len(results) == 0 {
		return 0, nil
	}

	subIDs := make([]string, len(results))
	statuses := make([]string, len(results))
	for i, res := range results {
		subIDs[i] = res.id
		statuses[i] = string(res.status)
	}

	const batchUpdateMCQ = `
		UPDATE submissions s
		SET status = t.status::submission_status
		FROM unnest($1::text[], $2::text[]) AS t(id, status)
		WHERE s.id = t.id
	`
	if _, err := tx.ExecContext(ctx, batchUpdateMCQ, pq.Array(subIDs), pq.Array(statuses)); err != nil {
		return 0, fmt.Errorf("batch update MCQ submissions: %w", err)
	}

	return len(results), nil
}

func (s *RankingStore) computeAndSaveRanksTx(ctx context.Context, tx *sql.Tx, contestID string) (int, error) {
	const problemsQ = `SELECT id, score FROM problems WHERE contest_id = $1`
	pRows, err := tx.QueryContext(ctx, problemsQ, contestID)
	if err != nil {
		return 0, fmt.Errorf("query problems: %w", err)
	}
	defer pRows.Close()

	problemScores := make(map[string]int)
	for pRows.Next() {
		var pid string
		var pscore int
		if err := pRows.Scan(&pid, &pscore); err != nil {
			return 0, fmt.Errorf("scan problem: %w", err)
		}
		problemScores[pid] = pscore
	}
	if err := pRows.Err(); err != nil {
		return 0, fmt.Errorf("problems iteration: %w", err)
	}

	participantSet := make(map[string]bool)

	regRows, err := tx.QueryContext(ctx, `SELECT user_id FROM contest_registrations WHERE contest_id = $1`, contestID)
	if err != nil {
		return 0, fmt.Errorf("query contest registrations: %w", err)
	}
	for regRows.Next() {
		var uid string
		if err := regRows.Scan(&uid); err != nil {
			regRows.Close()
			return 0, fmt.Errorf("scan registration: %w", err)
		}
		participantSet[uid] = true
	}
	if err := regRows.Err(); err != nil {
		regRows.Close()
		return 0, fmt.Errorf("registrations iteration: %w", err)
	}
	regRows.Close()

	subUserRows, err := tx.QueryContext(ctx, `SELECT DISTINCT user_id FROM submissions WHERE contest_id = $1`, contestID)
	if err != nil {
		return 0, fmt.Errorf("query submission users: %w", err)
	}
	for subUserRows.Next() {
		var uid string
		if err := subUserRows.Scan(&uid); err != nil {
			subUserRows.Close()
			return 0, fmt.Errorf("scan submission user: %w", err)
		}
		participantSet[uid] = true
	}
	if err := subUserRows.Err(); err != nil {
		subUserRows.Close()
		return 0, fmt.Errorf("submission users iteration: %w", err)
	}
	subUserRows.Close()

	rankUserRows, err := tx.QueryContext(ctx, `SELECT user_id FROM rankings WHERE contest_id = $1`, contestID)
	if err != nil {
		return 0, fmt.Errorf("query ranking users: %w", err)
	}
	for rankUserRows.Next() {
		var uid string
		if err := rankUserRows.Scan(&uid); err != nil {
			rankUserRows.Close()
			return 0, fmt.Errorf("scan ranking user: %w", err)
		}
		participantSet[uid] = true
	}
	if err := rankUserRows.Err(); err != nil {
		rankUserRows.Close()
		return 0, fmt.Errorf("rankings users iteration: %w", err)
	}
	rankUserRows.Close()

	if len(participantSet) == 0 {
		return 0, nil
	}

	const submissionsQ = `
		SELECT user_id, problem_id, status, created_at
		FROM submissions
		WHERE contest_id = $1
		ORDER BY created_at ASC
	`
	sRows, err := tx.QueryContext(ctx, submissionsQ, contestID)
	if err != nil {
		return 0, fmt.Errorf("query contest submissions: %w", err)
	}
	defer sRows.Close()

	type userSolveData struct {
		earliestAccepted map[string]int64
		totalAttempts    int
	}

	userStats := make(map[string]*userSolveData)
	for uid := range participantSet {
		userStats[uid] = &userSolveData{
			earliestAccepted: make(map[string]int64),
		}
	}

	for sRows.Next() {
		var uid, pid, status string
		var createdAt int64
		if err := sRows.Scan(&uid, &pid, &status, &createdAt); err != nil {
			return 0, fmt.Errorf("scan submission: %w", err)
		}
		data, ok := userStats[uid]
		if !ok {
			data = &userSolveData{earliestAccepted: make(map[string]int64)}
			userStats[uid] = data
			participantSet[uid] = true
		}
		if status != string(models.JudgeError) {
			data.totalAttempts++
		}
		if status == string(models.Accepted) {
			if prev, exists := data.earliestAccepted[pid]; !exists || createdAt < prev {
				data.earliestAccepted[pid] = createdAt
			}
		}
	}
	if err := sRows.Err(); err != nil {
		return 0, fmt.Errorf("submissions iteration: %w", err)
	}

	existingFlags := make(map[string]struct {
		hidden       bool
		disqualified bool
		shortlisted  bool
	})
	flagRows, err := tx.QueryContext(ctx, `SELECT user_id, hidden, disqualified, shortlisted FROM rankings WHERE contest_id = $1`, contestID)
	if err != nil {
		return 0, fmt.Errorf("query ranking flags: %w", err)
	}
	for flagRows.Next() {
		var uid string
		var h, d, s bool
		if err := flagRows.Scan(&uid, &h, &d, &s); err != nil {
			flagRows.Close()
			return 0, fmt.Errorf("scan ranking flag row: %w", err)
		}
		existingFlags[uid] = struct {
			hidden       bool
			disqualified bool
			shortlisted  bool
		}{h, d, s}
	}
	if err := flagRows.Err(); err != nil {
		flagRows.Close()
		return 0, fmt.Errorf("ranking flags iteration: %w", err)
	}
	flagRows.Close()

	participants := make([]participantSummary, 0, len(participantSet))
	for uid := range participantSet {
		data := userStats[uid]
		score := 0
		solvedCount := 0
		var maxSolveTime int64

		for pid, solveTime := range data.earliestAccepted {
			if pts, ok := problemScores[pid]; ok {
				score += pts
				solvedCount++
				if solveTime > maxSolveTime {
					maxSolveTime = solveTime
				}
			}
		}

		flags := existingFlags[uid]
		participants = append(participants, participantSummary{
			UserID:             uid,
			TotalScore:         score,
			ProblemsSolved:     solvedCount,
			LastSubmissionTime: maxSolveTime,
			TotalAttempts:      data.totalAttempts,
			Hidden:             flags.hidden,
			Disqualified:       flags.disqualified,
			Shortlisted:        flags.shortlisted,
		})
	}

	SortParticipantsForRanking(participants)
	AssignRanks(participants)

	n := len(participants)
	userIDs := make([]string, n)
	scores := make([]int, n)
	ranks := make([]int, n)
	hiddens := make([]bool, n)
	disqualifieds := make([]bool, n)
	shortlisteds := make([]bool, n)

	for i, p := range participants {
		userIDs[i] = p.UserID
		scores[i] = p.TotalScore
		ranks[i] = p.Rank
		hiddens[i] = p.Hidden
		disqualifieds[i] = p.Disqualified
		shortlisteds[i] = p.Shortlisted
	}

	const batchUpsertRankQ = `
		INSERT INTO rankings (contest_id, user_id, score, rank, hidden, disqualified, shortlisted)
		SELECT 
			$1, 
			t.user_id, 
			t.score, 
			t.rank, 
			t.hidden, 
			t.disqualified, 
			t.shortlisted
		FROM unnest(
			$2::text[], 
			$3::int[], 
			$4::int[], 
			$5::boolean[], 
			$6::boolean[], 
			$7::boolean[]
		) AS t(user_id, score, rank, hidden, disqualified, shortlisted)
		ORDER BY t.user_id ASC
		ON CONFLICT (contest_id, user_id)
		DO UPDATE SET 
			score = EXCLUDED.score,
			rank = EXCLUDED.rank,
			hidden = rankings.hidden,
			disqualified = rankings.disqualified,
			shortlisted = rankings.shortlisted
	`

	if _, err := tx.ExecContext(ctx, batchUpsertRankQ, contestID, pq.Array(userIDs), pq.Array(scores), pq.Array(ranks), pq.Array(hiddens), pq.Array(disqualifieds), pq.Array(shortlisteds)); err != nil {
		return 0, fmt.Errorf("batch upsert rankings: %w", err)
	}

	return len(participants), nil
}
