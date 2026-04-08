## Out-of-Scope Categories

Karpenter is a node autoscaler. It provisions compute capacity so pods
can run and removes capacity that is no longer needed. The project
maintains a strong bias toward saying "no" to changes without broad
benefit, minimizing API surface, and preferring solutions that work
without user knobs.

The following categories are out of scope. Issues that fall into these
categories should be classified as `wont_do` with `action: reject`:

1. **Notification and alerting delivery.** Karpenter emits Kubernetes
   events, status conditions, and Prometheus metrics. Delivering
   notifications (webhooks, Slack, email, PagerDuty) belongs to external
   tooling that consumes these signals.

2. **Exposing internal allocation strategy knobs.** Karpenter controls
   EC2 Fleet allocation strategies internally (lowest-price for
   on-demand, price-capacity-optimized for spot). If the default strategy
   is wrong, the fix is to change the default for everyone. Adding a
   user-configurable field makes the scheduling and optimization process
   harder to reason about.

3. **Pod-level scheduling decisions.** Karpenter provisions nodes, not
   pods. Which pod runs on which node belongs to kube-scheduler. Features
   that influence pod placement, emit pod-level events for scheduling
   decisions, or duplicate scheduler functionality are out of scope.

4. **Provider-neutral features in the AWS provider.** Features that
   apply to all cloud providers belong in kubernetes-sigs/karpenter. The
   AWS provider implements cloud-specific behavior only.

5. **Passthrough configuration that bypasses Karpenter's abstraction.**
   The AWS provider presents an opinionated abstraction over launch
   templates and EC2 configuration. Bypassing this abstraction can leave
   Karpenter unaware of underlying node capabilities, leading to
   incorrect launch or scheduling decisions.

When classifying, check the issue against these categories before
assigning a kind. An issue that requests notification delivery is
`wont_do` regardless of how many comments or upvotes it has.
