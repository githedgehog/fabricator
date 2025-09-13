#!/bin/bash
# Monitor all workflows for actual pause activity

REPO="githedgehog/fabricator"
BRANCH="pau/ci_reltest_pause"  # Set to specific branch or leave empty for all branches

# Consolidated header
if [ -n "$BRANCH" ]; then
  echo "🔍 Monitoring for paused jobs (including queued) Repository: $REPO Branch: $BRANCH"
else
  echo "🔍 Monitoring for paused jobs (including queued) Repository: $REPO All branches"
fi

echo "===== Time: $(date) ====="

# Get recent workflow runs (both in-progress AND queued)
if [ -n "$BRANCH" ]; then
  jq_filter=".workflow_runs | map(select(.head_branch == \"$BRANCH\" and (.status == \"in_progress\" or .status == \"queued\"))) | .[0:10] | .[]"
else
  # Check branches that might have pause capability - include both statuses
  jq_filter=".workflow_runs | map(select((.status == \"in_progress\" or .status == \"queued\") and (.head_branch | test(\"pause|debug|main|master\")))) | .[0:10] | .[]"
fi

found_any=false
found_paused=false

# Get workflows into an array instead of subshell
workflows=$(gh api repos/$REPO/actions/runs --jq "$jq_filter")

if [ -n "$workflows" ]; then
  # Process each workflow
  while IFS= read -r run; do
    if [ -z "$run" ]; then continue; fi
    
    found_any=true
    
    run_id=$(echo "$run" | jq -r '.id')
    name=$(echo "$run" | jq -r '.name')
    head_branch=$(echo "$run" | jq -r '.head_branch')
    created_at=$(echo "$run" | jq -r '.created_at')
    url=$(echo "$run" | jq -r '.html_url')
    workflow_status=$(echo "$run" | jq -r '.status')
    
    # Calculate runtime
    created_timestamp=$(date -d "$created_at" +%s 2>/dev/null || date -j -f "%Y-%m-%dT%H:%M:%SZ" "$created_at" +%s)
    current_timestamp=$(date +%s)
    runtime_minutes=$(( (current_timestamp - created_timestamp) / 60 ))
    
    # Workflow status icon
    case $workflow_status in
      "queued") workflow_icon="⏳" ;;
      "in_progress") workflow_icon="🟡" ;;
      *) workflow_icon="❓" ;;
    esac
    
    echo ""
    echo "$workflow_icon Workflow: $name ($workflow_status)"
    if [ -z "$BRANCH" ]; then
      echo "   Branch: $head_branch"
    fi
    echo "   Run ID: $run_id"
    echo "   Runtime: ${runtime_minutes} minutes"
    echo "   URL: $url"
    
    # Get ALL jobs for this workflow and filter for pausable ones (v-*, h-*)
    jobs_json=$(gh api repos/$REPO/actions/runs/$run_id/jobs --jq '.jobs[] | select(.name | test("^(v-|h-)"))')
    
    if [ -n "$jobs_json" ]; then
      # Count jobs by status using the filtered JSON
      total_jobs=$(echo "$jobs_json" | jq -s 'length')
      queued_count=$(echo "$jobs_json" | jq -s 'map(select(.status == "queued")) | length')
      in_progress_count=$(echo "$jobs_json" | jq -s 'map(select(.status == "in_progress")) | length')
      completed_count=$(echo "$jobs_json" | jq -s 'map(select(.status == "completed")) | length')
      failed_count=$(echo "$jobs_json" | jq -s 'map(select(.conclusion == "failure")) | length')
      
      echo "   Jobs: $total_jobs total ($completed_count ✅, $in_progress_count 🟡, $queued_count ⏳, $failed_count ❌)"
      
      # Process jobs in parallel to speed up log fetching
      temp_dir=$(mktemp -d)
      job_counter=0
      
      # Start parallel job processing
      while IFS= read -r job; do
        if [ -z "$job" ]; then continue; fi
        ((job_counter++))
        
        # Process each job in background
        (
          job_name=$(echo "$job" | jq -r '.name')
          job_status=$(echo "$job" | jq -r '.status')
          job_conclusion=$(echo "$job" | jq -r '.conclusion')
          job_started_at=$(echo "$job" | jq -r '.started_at')
          job_url=$(echo "$job" | jq -r '.html_url')
          job_id=$(echo "$job" | jq -r '.id')
          runner_name=$(echo "$job" | jq -r '.runner_name // empty')
          
          # Job status icon
          case $job_status in
            "queued") 
              status_icon="⏳" 
              ;;
            "in_progress") 
              status_icon="🟡" 
              ;;
            "completed") 
              if [ "$job_conclusion" = "success" ]; then
                status_icon="✅"
              elif [ "$job_conclusion" = "failure" ]; then
                status_icon="❌"
              elif [ "$job_conclusion" = "cancelled" ]; then
                status_icon="🚫"
              else
                status_icon="✅"
              fi
              ;;
            *) 
              status_icon="❓" 
              ;;
          esac
          
          # Clean up job name - remove common suffixes
          clean_job_name=$(echo "$job_name" | sed 's/-rt \/ run$//' | sed 's/ \/ run$//')
          
          # Build job line with runner info and runtime if available
          job_output=""
          if [ "$job_started_at" != "null" ] && [ "$job_status" = "in_progress" ]; then
            job_started_timestamp=$(date -d "$job_started_at" +%s 2>/dev/null || date -j -f "%Y-%m-%dT%H:%M:%SZ" "$job_started_at" +%s)
            job_runtime_minutes=$(( (current_timestamp - job_started_timestamp) / 60 ))
            if [ -n "$runner_name" ]; then
              job_output+="      $status_icon $clean_job_name (Runner: '$runner_name') (${job_runtime_minutes}min)"
            else
              job_output+="      $status_icon $clean_job_name (${job_runtime_minutes}min)"
            fi
          else
            if [ -n "$runner_name" ]; then
              job_output+="      $status_icon $clean_job_name (Runner: '$runner_name')"
            else
              job_output+="      $status_icon $clean_job_name"
            fi
          fi
          
          # Show URL on next line after job info
          job_output+="\n         → $job_url"
          
          # Check pause history for ANY job that has started (running, completed, or failed)
          if [ "$job_started_at" != "null" ] && [ "$job_status" != "queued" ]; then
            logs=$(gh api repos/$REPO/actions/jobs/$job_id/logs 2>/dev/null || echo "")
            
            if echo "$logs" | grep -q "Pausing for debugging"; then
              # Extract pause duration if available
              pause_duration=$(echo "$logs" | grep -o "duration=[0-9hms]*" | head -1 | cut -d'=' -f2)
              
              if echo "$logs" | grep -q "Pause duration expired"; then
                # Extract pause timestamps for timeline
                pause_start=$(echo "$logs" | grep "Pausing for debugging" | head -1 | grep -o "^[0-9T:\.-]*Z" | head -1)
                pause_end=$(echo "$logs" | grep "Pause duration expired" | head -1 | grep -o "^[0-9T:\.-]*Z" | head -1)
                
                if [ -n "$pause_start" ] && [ -n "$pause_end" ]; then
                  job_output+="\n         📅 WAS PAUSED ($(echo $pause_start | cut -d'T' -f2 | cut -d'.' -f1) → $(echo $pause_end | cut -d'T' -f2 | cut -d'.' -f1))"
                else
                  job_output+="\n         📅 WAS PAUSED (${pause_duration:-"?"})"
                fi
              else
                if [ "$job_status" = "in_progress" ]; then
                  job_output+="\n         🚨 PAUSED NOW (${pause_duration:-"?"})"
                else
                  job_output+="\n         🚨 WAS PAUSED (${pause_duration:-"?"})"
                fi
                
                # Extract pause start time
                pause_start=$(echo "$logs" | grep "Pausing for debugging" | head -1 | grep -o "^[0-9T:\.-]*Z" | head -1)
                if [ -n "$pause_start" ]; then
                  job_output+="\n         📅 Started: $(echo $pause_start | cut -d'T' -f2 | cut -d'.' -f1)"
                fi
              fi
            fi
          fi
          
          # Write output to temp file with job order preserved - add single newline at end
          echo -e "$job_output" > "$temp_dir/job_$job_counter"
        ) &
        
        # Limit concurrent processes to avoid API rate limits
        if (( job_counter % 8 == 0 )); then
          wait
        fi
      done <<< "$jobs_json"
      
      # Wait for all jobs to complete
      wait
      
      # Output results in original order with no extra blank lines
      for ((i=1; i<=job_counter; i++)); do
        if [ -f "$temp_dir/job_$i" ]; then
          cat "$temp_dir/job_$i"
        fi
      done
      
      # Clean up
      rm -rf "$temp_dir"
    else
      echo "   No pausable jobs (v-*, h-*) found in this workflow"
    fi
  done <<< "$workflows"
fi

if [ "$found_any" = "false" ]; then
  echo ""
  echo "✅ No running or queued workflows found"
fi

echo ""
echo "=== Updated: $(date +'%H:%M:%S') | ⏳ Queued 🟡 Running ✅ Success ❌ Failed 🚫 Cancelled 🚨 Paused ==="
