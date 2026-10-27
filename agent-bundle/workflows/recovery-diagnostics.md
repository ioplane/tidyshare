# Recovery and blocked diagnostics

Stop on `UNKNOWN`, `BLOCKED`, stale plan, identity mismatch, cancellation, or a partial result. Preserve private reports, the exact plan, journal, recovery paths and any retained scratch roots. Check whether owned processes stopped using the evidence available to the operator. Preserve uncertainty if they cannot be proven stopped.

Do not automatically retry execution. Inspect the result and agree on a separate recovery action. Do not signal a process group borrowed from another process. Do not bypass Darwin guards, permissions or SELinux. Do not relabel user data or disable SELinux. Do not use sudo as a workaround. A retained file may need recovery from its reported path. A native operation has no per-file deletion journal.

On Linux, system open-file observation can be incomplete when other mounts are
not readable. Keep the blocked result. Do not suppress observer warnings to
permit execution. Use a private namespace only to qualify
test-created fixtures. Never use it to hide host consumers of real user caches.

A failed scan can retain a private staging directory under its MCP report
directory. Follow the error's reported path and treat those files as partial
evidence. Do not assume that retained files form a complete successful report.
