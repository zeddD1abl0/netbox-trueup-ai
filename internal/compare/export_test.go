package compare

import "github.com/zeddD1abl0/netbox-trueup-ai/internal/model"

// MergeRows merges the rows of results' comparisons of, by their index, as
// Objects does for an object with more than one definition, so that tests
// can merge one comparison too.
func MergeRows(specs []*model.Spec, results []Result, of []int) ([]Row, error) {
	return mergeRows(specs, model.SpecOf(specs, results[of[0]].Type), results, of)
}
