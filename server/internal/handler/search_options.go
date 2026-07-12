package handler

import (
	"net/url"
	"strconv"
)

type searchQueryOptions struct {
	limit         int
	offset        int
	includeClosed bool
}

func parseSearchQueryOptions(values url.Values) searchQueryOptions {
	options := searchQueryOptions{
		limit:         20,
		includeClosed: values.Get("include_closed") == "true",
	}
	if value := values.Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			options.limit = min(parsed, 50)
		}
	}
	if value := values.Get("offset"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			options.offset = parsed
		}
	}
	return options
}
