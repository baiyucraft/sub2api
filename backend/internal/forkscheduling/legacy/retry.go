package legacy

import "sort"

var DefaultPoolModeRetryStatusCodes = []int{401, 403, 429}

func NormalizePoolModeRetryStatusCodes(codes []int) ([]int, error) {
	seen := make(map[int]struct{}, len(codes))
	normalized := make([]int, 0, len(codes))
	for _, code := range codes {
		if code < 100 || code > 599 {
			return nil, ErrInvalidStatusCode
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		normalized = append(normalized, code)
	}
	sort.Ints(normalized)
	return normalized, nil
}

func RetryableStatus(codes []int, statusCode int) bool {
	for _, code := range codes {
		if code == statusCode {
			return true
		}
	}
	return false
}
