# GitHub Traffic Metrics

This directory stores historical GitHub traffic metrics for the repository, preserving data beyond GitHub's built-in 14-day retention window.

## Directory Structure

```
metrics/
└── traffic/
    ├── .gitkeep
    ├── 2025-10-30.json
    ├── 2025-10-31.json
    └── ...
```

## Automated Collection

The [traffic-metrics workflow](../.github/workflows/traffic-metrics.yml) runs daily at 00:00 UTC and collects:
- **Views**: Total and unique page views over the last 14 days
- **Clones**: Total and unique repository clones over the last 14 days
- **Top Referrers**: Sources driving traffic to the repository
- **Popular Paths**: Most visited pages in the repository

Each snapshot is saved as `YYYY-MM-DD.json` with the collection date.

## Manual Collection

You can trigger the workflow manually:
1. Go to the [Actions tab](../../actions/workflows/traffic-metrics.yml)
2. Click "Run workflow"
3. Select the branch and click "Run workflow"

## Viewing Metrics

Use the provided Python script to view a summary of the most recent metrics:

```bash
python3 scripts/summarize_traffic.py
```

Example output:
```
======================================================================
GitHub Traffic Metrics Summary
======================================================================
Repository: etalazz/vsa
Snapshot Date: 2025-10-30
Collected At: 2025-10-30T18:00:00.000Z
======================================================================

📊 VIEWS
  Total Views: 1,250
  Unique Visitors: 145
  Daily Average: 89.3 views, 10.4 unique
  Period: 14 days

📦 CLONES
  Total Clones: 45
  Unique Cloners: 12
  Daily Average: 3.2 clones, 0.9 unique
  Period: 14 days

🔗 TOP REFERRERS
   1. github.com                                    450 views,     45 unique
   2. google.com                                    280 views,     38 unique
   ...

📄 POPULAR PATHS
   1. /etalazz/vsa (etalazz/vsa: Vector-Scalar Accumulator)             425 views,     78 unique
   ...
```

## Data Format

Each JSON snapshot contains:

```json
{
  "date": "2025-10-30",
  "timestamp": "2025-10-30T18:00:00.000Z",
  "repository": "etalazz/vsa",
  "views": {
    "count": 1250,
    "uniques": 145,
    "views": [
      {
        "timestamp": "2025-10-16T00:00:00Z",
        "count": 85,
        "uniques": 12
      },
      ...
    ]
  },
  "clones": { ... },
  "referrers": [ ... ],
  "paths": [ ... ]
}
```

## Notes

- GitHub's traffic API only provides data for the last 14 days
- The workflow requires `contents: write` permission to commit snapshots
- Snapshots are automatically committed back to the repository
- Historical analysis requires accumulating daily snapshots over time
