---
name: fanout
description: Split a job across several Runlease machines and collect the results. Use for test shards, a matrix of versions or configurations, or independent tasks that can run at the same time.
---

# Fan out, then let them all end

Every machine carries its own lease, so there is no cleanup job to write. There is still a bill:
decide the number of machines from the work, and tell the person the count and the size before
launching more than two.

## Before launching

```bash
runlease whoami        # the relay's limit: "N of M machines in use"
```

The free public relay allows two machines at once. Asking for a third fails before anything is
created. On the person's own relay the limit is theirs.

## Pattern

```bash
N=2
for i in $(seq 1 $N); do runlease up -p gcp -n shard-$i --ttl 1h -d; done

# wait until each answers (30-60s after launch)
for i in $(seq 1 $N); do
  until runlease exec shard-$i -- true 2>/dev/null; do sleep 10; done
done

# send the work, run the shards at the same time, each writing its own result file
for i in $(seq 1 $N); do
  runlease push shard-$i /tmp/src.tar.gz /tmp/src.tar.gz
  runlease exec shard-$i -- bash -c "mkdir -p ~/src && tar -xzf /tmp/src.tar.gz -C ~/src && cd ~/src && SHARD=$i TOTAL=$N ./test.sh > ~/shard.log 2>&1; echo exit=\$? >> ~/shard.log" &
done
wait

for i in $(seq 1 $N); do runlease pull shard-$i /home/runlease-agent/shard.log ./shard-$i.log; done
for i in $(seq 1 $N); do runlease down shard-$i --yes; done
```

## Rules

- Name machines by role and index (`shard-1`, `py311`), never reuse one name for two machines.
- One launch that fails must not strand the others: after any failure, run `runlease list` and take
  down what was created.
- Pull each result before deleting its machine. A spot machine can also disappear on its own, so a
  missing shard is a result to report, not a reason to rerun everything.
- Report the outcome per machine: passed, failed, or lost.
