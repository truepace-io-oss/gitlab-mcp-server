package gitlab

import (
	"context"
	"net/url"
	"strconv"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// ListAll walks an offset-paginated collection up to the configured page cap and
// reports whether the cap was hit. Callers MUST surface `truncated` to the model
// — a silently partial list reads as a complete one.
func ListAll[T any](ctx context.Context, c *Client, cap perm.Capability, path string, query url.Values, limit int) (items []T, truncated bool, err error) {
	if query == nil {
		query = url.Values{}
	}
	perPage := c.Page.PerPage
	if limit > 0 && limit < perPage {
		perPage = limit
	}
	query.Set("per_page", strconv.Itoa(perPage))

	for page := 1; page <= c.Page.MaxPages; page++ {
		q := cloneValues(query)
		q.Set("page", strconv.Itoa(page))

		var batch []T
		resp, err := c.DoList(ctx, cap, path, q, &batch)
		if err != nil {
			return nil, false, err
		}
		items = append(items, batch...)

		if limit > 0 && len(items) >= limit {
			return items[:limit], resp.NextPage != "" || len(items) > limit, nil
		}
		// An empty page or no next-page header means we are done.
		if len(batch) == 0 || resp.NextPage == "" {
			return items, false, nil
		}
		if page == c.Page.MaxPages {
			// More pages exist but the cap is reached.
			return items, true, nil
		}
	}
	return items, true, nil
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, v := range in {
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}
