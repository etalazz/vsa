# GitHub Traffic Metrics

This directory contains automated GitHub traffic metrics snapshots collected from the repository.

## Overview

The traffic metrics system:
- Automatically collects traffic data daily via GitHub Actions
- Stores snapshots as JSON files named `YYYY-MM-DD.json`
- Preserves data beyond GitHub's built-in 14-day window
- Enables historical analysis and trend tracking

## Data Structure

Each JSON snapshot contains:
- **timestamp**: When the snapshot was collected
- **repository**: Repository name (owner/repo)
- **views**: View counts and detailed daily breakdown
- **clones**: Clone counts and detailed daily breakdown
- **referrers**: Top referral sources with counts
- **paths**: Most popular repository paths

## Usage

### Automatic Collection

The workflow runs automatically:
- **Daily**: Every day at 00:00 UTC
- **Manual**: Via workflow_dispatch (Actions tab → Traffic Metrics → Run workflow)

### Analyzing Data Locally

Use the Python script to summarize the most recent snapshot:

```bash
# Summarize the latest snapshot
python scripts/summarize_traffic.py

# Summarize a specific snapshot
python scripts/summarize_traffic.py --file 2025-10-30.json

# Show help
python scripts/summarize_traffic.py --help
```

The script displays:
- Total and average views/clones
- Top referrers (external sites driving traffic)
- Most popular paths (pages visitors view most)

## Example Output

```
======================================================================
GitHub Traffic Metrics Summary
======================================================================

Repository: etalazz/vsa
Snapshot timestamp: 2025-10-30T18:00:00.000Z

VIEWS
----------------------------------------------------------------------
  Total count: 1,523
  Total uniques: 234
  Daily average count: 217.6
  Daily average uniques: 33.4
  Days tracked: 7

CLONES
----------------------------------------------------------------------
  Total count: 87
  Total uniques: 42
  Daily average count: 12.4
  Daily average uniques: 6.0
  Days tracked: 7

TOP REFERRERS (5 total)
----------------------------------------------------------------------
   1. github.com                               count:   543  uniques:    87
   2. google.com                               count:   234  uniques:    54
   3. reddit.com                               count:   189  uniques:    43
```

## Historical Analysis

Compare snapshots over time to:
- Track growth trends
- Identify traffic spikes and their sources
- Measure impact of releases or announcements
- Understand visitor behavior patterns

## Files

- `YYYY-MM-DD.json`: Daily traffic snapshots
- `README.md`: This file
