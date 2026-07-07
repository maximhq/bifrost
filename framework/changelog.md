[fix]: keep the streaming finish_reason in the accumulated response when a provider forwards it on a content chunk, so logging and plugins record the real stop reason (closes #4963) [@fus3r](https://github.com/fus3r)
[fix]: sweep orphaned deferred spans in trace store TTL cleanup [@citrocat](https://github.com/citrocat)
[fix]: rebuild token usage from denormalized columns in hybrid log list [@G-XD](https://github.com/G-XD)
