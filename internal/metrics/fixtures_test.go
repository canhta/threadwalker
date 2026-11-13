package metrics_test

import (
	"context"

	"github.com/canhta/threadz/internal/trends"
)

func metricTrends(context.Context) ([]trends.Trend, error) { return []trends.Trend{}, nil }

func metricStudy(context.Context) (trends.StudyFeed, error) { return trends.StudyFeed{}, nil }
