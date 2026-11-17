package acceptance

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseItems parses the -items flag of the live command: a comma-separated list of acceptance item
// numbers 1-8, whitespace around a number allowed, order kept. An empty list, an empty element or any
// other value is an error. The command calls it before it creates anything, so an invalid flag leaves
// no evidence file behind.
func ParseItems(s string) ([]int, error) {
	var nums []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 8 {
			return nil, fmt.Errorf("acceptance: items must be numbers 1-8")
		}
		nums = append(nums, n)
	}
	return nums, nil
}
