# Phase 27: Documentation & Hardware Validation - Research

**Researched:** 2026-02-05
**Domain:** Technical Documentation, Hardware Validation, Testing Workflows
**Confidence:** HIGH

## Summary

Phase 27 consolidates testing and deployment knowledge from v0.9.0 development into comprehensive documentation that enables contributors to test effectively and operators to validate hardware deployments. This phase is unique because the user has live production cluster access with real RDS hardware, providing the perfect opportunity to validate assumptions against reality and document actual hardware behavior.

The documentation challenge is multi-faceted:
1. **Hardware validation** requires step-by-step procedures that non-experts can execute against real hardware
2. **Testing workflows** must bridge the gap between automated CI and manual hardware testing
3. **CSI capability gaps** need honest assessment against mature drivers like AWS EBS CSI and Longhorn
4. **Known limitations** must be specific enough to guide deployment decisions
5. **Troubleshooting** requires diagnostic flows that lead to solutions, not just symptom descriptions
6. **Snapshot documentation** is forward-looking (Phase 26 just completed) and needs examples before real-world usage exists
7. **CI/CD documentation** must remain maintainable as the pipeline evolves

Existing documentation is strong in some areas (architecture.md, TESTING.md, ci-cd.md) but lacks hardware validation procedures and comprehensive gap analysis. The project has excellent test infrastructure (68.6% coverage, mock RDS, E2E tests) but documentation hasn't caught up with what's been learned through production deployment.

**Primary recommendation:** Document-as-you-validate approach where hardware testing procedures are written by actually executing them against the production cluster, capturing real output, timing, and failure modes.

## Standard Stack

The established tools/patterns for technical documentation in the CSI ecosystem:

### Core Documentation Tools
| Tool | Purpose | Why Standard |
|------|---------|--------------|
| Markdown | All documentation format | GitHub-native, version-controlled, diff-friendly |
| GitHub Actions badges | Build/test status visibility | Real-time status in README |
| Examples directory | Runnable YAML manifests | Users learn by example, not just reading |
| Mermaid diagrams | Architecture visualization | Renders in GitHub, version-controlled |

### Documentation Structure Patterns
| Pattern | Purpose | Examples |
|---------|---------|----------|
| README.md as entry point | Quick start and navigation hub | AWS EBS CSI, Longhorn |
| docs/ directory | Deep-dive documentation | Standard across CSI drivers |
| TESTING.md | Contributor test guide | Kubernetes-CSI docs |
| Known limitations section | Honest capability assessment | IBM Block CSI, Azure Blob CSI |
| examples/ with comments | Runnable configurations | All major CSI drivers |

### Testing Documentation Standards
| Standard | Purpose | Source |
|----------|---------|--------|
| Unit/Integration/E2E taxonomy | Clear test layer definitions | Kubernetes-CSI functional testing docs |
| Hardware test prerequisites | Environment setup requirements | OpenShift CSI verification |
| Test execution matrix | What runs where (CI vs manual) | Dell cert-csi framework |
| Troubleshooting by symptom | User-facing diagnostic flows | Longhorn troubleshooting docs |

**Key Insight:** Mature CSI drivers separate "contributor testing" (unit/integration/sanity in CI) from "operator validation" (hardware deployment testing). RDS CSI has excellent contributor testing docs (TESTING.md) but lacks operator validation guide.

## Architecture Patterns

### Pattern 1: Hardware Validation Guide Structure
**What:** Step-by-step manual testing procedures that validate driver behavior against real hardware
**When to use:** Initial deployment, after upgrades, troubleshooting production issues
**Structure:**
```markdown
HARDWARE_VALIDATION.md
├── Prerequisites (hardware, network, credentials)
├── Environment Validation (verify cluster/RDS readiness)
├── Test Scenarios
│   ├── TC-01: Basic Volume Lifecycle
│   │   ├── Objective
│   │   ├── Prerequisites
│   │   ├── Steps (with expected output)
│   │   ├── Success Criteria
│   │   └── Cleanup
│   ├── TC-02: NVMe/TCP Connection Validation
│   ├── TC-03: Volume Expansion
│   ├── TC-04: KubeVirt VM Boot & Migration
│   ├── TC-05: Orphan Reconciliation
│   └── TC-06: Failure Recovery (RDS restart, network interruption)
├── Performance Baselines (expected latency/throughput)
└── Troubleshooting Decision Tree
```

**Best Practices:**
- Start each test case with clear objective and expected outcome
- Include exact commands with expected output samples
- Provide cleanup steps even if test fails mid-way
- Document timing expectations (e.g., "volume creation: 10-30s")
- Include "what to check when it fails" for each step
- Use actual production output/logs as examples

**Source:** Hardware verification protocols (NASA V&V guidelines), HIL testing documentation

### Pattern 2: Testing Guide Organization
**What:** Comprehensive contributor testing guide covering test types, execution, and interpretation
**Structure:**
```markdown
TESTING.md
├── Overview (test pyramid, coverage goals)
├── Quick Start (run all tests locally)
├── Test Types
│   ├── Unit Tests (scope, how to run, add new tests)
│   ├── Integration Tests (mock RDS, scenarios)
│   ├── Sanity Tests (CSI compliance, interpretation)
│   ├── E2E Tests (full stack, hardware requirements)
│   └── Performance Tests (benchmarking procedures)
├── CI/CD Integration (what runs when, interpreting failures)
├── Debugging Test Failures
│   ├── Common Failure Patterns (with solutions)
│   ├── Log Analysis (where to look, what patterns mean)
│   └── Test Infrastructure Issues (mock-reality divergence)
├── Contributing Tests (guidelines, patterns, anti-patterns)
└── Coverage Goals (by package, acceptable gaps)
```

**Best Practices from Kubernetes-CSI:**
- Start with "how to run" before "how to add"
- Separate local testing from CI testing procedures
- Include "why this test failed" for common failures
- Document test environment assumptions (kernel version, nvme-cli, etc.)
- Provide examples of good test patterns
- Be explicit about hardware-dependent code that can't be unit tested

**Sources:**
- [Kubernetes CSI Testing Drivers Guide](https://kubernetes-csi.github.io/docs/testing-drivers.html)
- [Kubernetes CSI Functional Testing](https://kubernetes-csi.github.io/docs/functional-testing.html)
- RDS CSI existing TESTING.md (good foundation, needs hardware validation section)

### Pattern 3: Capability Gap Analysis Framework
**What:** Structured comparison of CSI capabilities vs peer drivers to set user expectations
**Structure:**
```markdown
CAPABILITIES.md
├── Overview (driver maturity, production status)
├── CSI Specification Coverage
│   ├── Identity Service (✅ Complete)
│   ├── Controller Service (capabilities + gaps)
│   └── Node Service (capabilities + gaps)
├── Feature Comparison Matrix
│   ├── vs AWS EBS CSI (snapshot strategies, volume types)
│   ├── vs Longhorn (distributed storage, HA)
│   └── vs SPDK CSI (NVMe/TCP implementation)
├── Advanced Features
│   ├── Snapshots (Phase 26 - status, limitations)
│   ├── Cloning (planned/not planned)
│   ├── Volume Migration (supported/not supported)
│   └── Topology (current state)
├── Operational Capabilities
│   ├── High Availability (single controller, implications)
│   ├── Scalability Limits (max volumes, performance)
│   └── Multi-tenancy (supported/not supported)
└── Roadmap (what's planned, timeline)
```

**Gap Analysis Best Practices:**
- Be honest about limitations, not defensive
- Explain "why not" for missing features (architectural, planned, won't-fix)
- Compare apples-to-apples (single-node RDS vs single-node Longhorn, not vs distributed)
- Highlight unique advantages (NVMe/TCP latency, RouterOS integration)
- Link to issues/roadmap for planned features

**Examples from Peer Drivers:**
- IBM Block CSI: Clear limitations section with version dependencies
- Secrets Store CSI: Known limitations page with workarounds
- Azure Blob CSI: Explicit concurrency limitations with architectural explanation

**Sources:**
- [IBM Block Storage CSI Limitations](https://www.ibm.com/docs/en/stg-block-csi-driver/1.11.4?topic=notes-limitations)
- [Secrets Store CSI Known Limitations](https://secrets-store-csi-driver.sigs.k8s.io/known-limitations)

### Pattern 4: Known Limitations Documentation
**What:** Specific, actionable limitations that affect deployment decisions
**Anti-Pattern:** Vague statements like "may not work in all environments"
**Good Pattern:** Specific constraints with version numbers and workarounds

**Structure:**
```markdown
## Known Limitations

### RouterOS Version Compatibility
**Limitation:** Requires RouterOS 7.1+ with ROSE Data Server feature
**Impact:** Cannot deploy on RouterOS 6.x or CHR (Cloud Hosted Router) versions
**Workaround:** Upgrade to RouterOS 7.x with RDS hardware
**Affected Components:** All (SSH CLI command syntax)

### NVMe Device Timing Assumptions
**Limitation:** NodeStageVolume assumes device appears within 30 seconds
**Impact:** On slow networks or heavily loaded RDS, volume attach may fail
**Workaround:** Configure `nvme-connect-timeout-seconds` in StorageClass (60s+ recommended)
**Affected Components:** Node plugin, NVMe connection manager
**Detection:** "timeout waiting for device" in node logs

### Dual-IP Architecture Requirements
**Limitation:** Optimal performance requires separate management and storage networks
**Impact:** Single-IP deployments work but management SSH traffic competes with storage I/O
**Workaround:** Can use same IP, but recommend separate VLANs for production
**Affected Components:** Controller (SSH), Node (NVMe/TCP)
**Configuration:** Set `nvmeAddress` != `rdsAddress` in StorageClass
```

**Key Principles:**
- Start with version-specific requirements
- Include detection methods (how to know if you're hitting this limitation)
- Provide workarounds even if not ideal
- Link to issues/discussions for context
- Update as limitations are addressed

**Sources:**
- CSI driver limitations examples across IBM, Azure, AWS drivers
- RDS CSI actual limitations discovered during v0.6-v0.9 development

### Pattern 5: Troubleshooting Guide Structure
**What:** Symptom-driven diagnostic flows that lead to solutions, not tech dumps
**Anti-Pattern:** "Check logs" without saying what to look for
**Good Pattern:** Decision tree from user-observable symptoms to root cause

**Structure:**
```markdown
## Troubleshooting

### Volume Stuck in Pending
**Symptom:** PVC shows "Pending" for >5 minutes
**Diagnostic Steps:**
1. Check controller pod status: `kubectl get pods -n kube-system -l app=rds-csi-controller`
   - If CrashLoopBackOff → See "Controller Won't Start"
   - If Running → Continue to step 2
2. Check controller logs for RDS connection: `kubectl logs -n kube-system -l app=rds-csi-controller -c rds-csi-plugin | grep "CreateVolume"`
   - If "SSH connection failed" → See "SSH Authentication Issues"
   - If "not enough space" → See "Insufficient Storage"
   - If no logs → Check event on PVC
3. Check PVC events: `kubectl describe pvc <name>`
   - Look for "failed to provision volume" with specific error

**Common Causes:**
- SSH key authentication failure (fix: verify Secret)
- RDS disk full (fix: free space on RDS)
- Network connectivity to RDS (fix: verify routes)

**Related:** SSH Authentication Issues, Insufficient Storage
```

**Best Practices:**
- Start with what users see (error messages, kubectl output)
- Provide exact commands to gather diagnostic info
- Use conditional logic (if X, then Y)
- Cross-reference related issues
- Include "quick fix" first, then deep dive

**Sources:**
- [Longhorn Troubleshooting](https://longhorn.io/docs/1.9.1/troubleshoot/troubleshooting/)
- [IT Documentation Best Practices](https://www.ninjaone.com/blog/it-documentation-best-practices/)

### Pattern 6: Snapshot Documentation
**What:** Usage guide for Btrfs snapshot feature (Phase 26 just completed)
**Challenge:** Feature is new, limited real-world usage to draw from
**Approach:** Example-driven documentation with best practices from Btrfs/CSI ecosystem

**Structure:**
```markdown
docs/snapshots.md
├── Overview (Btrfs snapshot backing, use cases)
├── Prerequisites (VolumeSnapshotClass, snapshot-controller)
├── Quick Start
│   ├── Create VolumeSnapshotClass
│   ├── Create VolumeSnapshot
│   └── Restore from Snapshot
├── Use Cases
│   ├── Backup Before Upgrade (with timing expectations)
│   ├── Clone VM for Testing (with KubeVirt example)
│   └── Disaster Recovery (with RPO/RTO)
├── Best Practices
│   ├── Snapshot Frequency (Btrfs overhead)
│   ├── Retention Policies (space management)
│   └── Naming Conventions (tracking origin)
├── Limitations
│   ├── Same-RDS-only (no cross-RDS snapshots)
│   ├── Space Requirements (Btrfs COW behavior)
│   └── Performance Impact (snapshot count)
├── Examples (complete YAML manifests)
└── Troubleshooting (snapshot-specific issues)
```

**Content Strategy:**
- Use complete, runnable examples (not fragments)
- Explain timing (snapshot creation: <5s, restore: minutes)
- Document Btrfs-specific behavior (COW, space reclamation)
- Include negative examples (what doesn't work, why)
- Cross-reference KubeVirt migration documentation

**Sources:**
- [Kubernetes Volume Snapshots](https://kubernetes.io/docs/concepts/storage/volume-snapshots/)
- [Kubernetes CSI Volume Snapshot](https://kubernetes-csi.github.io/docs/snapshot-restore-feature.html)
- Btrfs snapshot behavior documentation

### Pattern 7: CI/CD Documentation Maintainability
**What:** Documentation that evolves with the pipeline, doesn't go stale
**Challenge:** CI workflows change frequently, docs become outdated
**Solution:** Describe patterns and principles, not exact steps

**Structure:**
```markdown
## CI/CD Documentation (in existing ci-cd.md)

### Adding a New Test Job
**Pattern:** Independent test jobs run in parallel for fast feedback
**When to add:** New test type that should gate merges (e.g., snapshot sanity tests)
**Template:**
```yaml
snapshot-sanity:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - name: Run snapshot sanity tests
      run: make test-sanity-snapshots
    - name: Upload logs on failure
      if: failure()
      uses: actions/upload-artifact@v4
```

**Key Decisions:**
- Run on all PRs vs main-only
- Timeout (default: 10min, adjust for test duration)
- Artifact upload on failure (always for new test types)

### Interpreting Test Results
**Job Failed:** What each job validates and how to debug
- `verify`: Code quality (lints, format)
  - Failure pattern: "golangci-lint errors found"
  - Fix: Run `make lint` locally, address issues
- `test`: Unit and integration tests
  - Failure pattern: "FAIL: TestXXX"
  - Fix: Run `make test` locally, check test output
- `sanity-tests`: CSI spec compliance
  - Failure pattern: "idempotency check failed"
  - Fix: See TESTING.md → CSI Sanity Tests → Common Failures

**Philosophy:** Document decision criteria and patterns, not exact YAML. Link to actual workflow files for specifics.
```

**Maintainability Strategies:**
- Link to workflow files (single source of truth)
- Document "why" not "what" (YAML shows what, docs explain why)
- Use workflow_dispatch examples for manual runs
- Version-tag documentation with workflow changes
- Automate documentation updates where possible (badge URLs, etc.)

**Sources:**
- [GitHub Actions CI/CD Best Practices](https://github.com/github/awesome-copilot/blob/main/instructions/github-actions-ci-cd-best-practices.instructions.md)
- [Building CI/CD with GitHub Actions](https://resources.github.com/learn/pathways/automation/essentials/building-a-workflow-with-github-actions/)
- Existing ci-cd.md (good foundation)

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Test scenario templates | Custom test format | Ginkgo BDD (Given/When/Then) | Already using Ginkgo, consistent with E2E tests |
| Diagram generation | PowerPoint/draw.io files | Mermaid in Markdown | Version-controlled, renders in GitHub, CI-verifiable |
| API documentation | Manual method descriptions | Godoc with examples | Auto-generated from code, stays in sync |
| Change tracking | Manual changelog | Conventional Commits + automation | Already used for releases, extend to docs |

**Key insight:** The project already uses Ginkgo/Gomega for E2E tests. Hardware validation procedures should follow similar BDD style for consistency (Scenario/Given/When/Then/Expected).

## Common Pitfalls

### Pitfall 1: Mock-Reality Divergence in Documentation
**What goes wrong:** Documentation describes mock behavior, not actual hardware behavior
**Why it happens:** Tests written against mock RDS, docs written from test code, never validated against real RDS
**How to avoid:** Execute every documented procedure against real hardware before committing
**Warning signs:** User reports "doesn't work on my cluster" with symptoms that shouldn't happen
**Example:** Mock returns instantly, docs say "volume creation takes 2-5s", reality is 10-30s on loaded RDS

### Pitfall 2: Version-Specific Documentation Without Versions
**What goes wrong:** Limitations described without specifying which versions they apply to
**Why it happens:** Rushing documentation, assuming "current version" is obvious
**How to avoid:** Always include version numbers, even for current limitations
**Warning signs:** Issue reports: "docs say X doesn't work, but I'm using version Y and it works"
**Example:** "Snapshots not supported" → should be "Snapshots supported in v0.9.0+, requires RouterOS 7.16+"

### Pitfall 3: Troubleshooting Without Verification Steps
**What goes wrong:** Troubleshooting guide suggests solutions but no way to verify they worked
**Why it happens:** Focus on fix, forget to explain how to confirm fix worked
**How to avoid:** Every troubleshooting step ends with "Verify: ..."
**Warning signs:** Users apply fix, still have problem, don't know if fix was right or wrong
**Example:** "Fix SSH auth by updating Secret" → add "Verify: Controller logs show 'Connected to RDS' after restart"

### Pitfall 4: Hardware Validation Without Cleanup Procedures
**What goes wrong:** Test procedures create resources, don't clean up, RDS fills up
**Why it happens:** Focus on happy path, assume tests will complete successfully
**How to avoid:** Every test case has explicit cleanup steps, executable even if test fails mid-way
**Warning signs:** "RDS is full" after running validation tests
**Example:** Hardware test creates 10GB volume, fails at step 3, cleanup steps are after step 5

### Pitfall 5: Snapshot Documentation Before Real Usage
**What goes wrong:** Documentation makes assumptions about usage patterns that don't match reality
**Why it happens:** Phase 26 just completed, snapshots are new, no production experience yet
**How to avoid:** Mark snapshot docs as "initial guidance", plan review after 30 days of production use
**Warning signs:** First snapshot user reports "docs don't mention X limitation" found in real usage
**Example:** Docs say snapshots are "fast", but 100GB snapshot takes 5 minutes in production

### Pitfall 6: Test-Only Features Documented as Production-Ready
**What goes wrong:** Features tested in CI but not validated on real hardware documented as working
**Why it happens:** CI passes, assume that means production-ready
**How to avoid:** Separate "tested in CI" from "validated on hardware" in documentation
**Warning signs:** Production incident from feature that "passed all tests"
**Example:** Block volume support passes mock E2E tests, but has timing issues on real NVMe/TCP

## Code Examples

### Hardware Validation Test Case Format

```markdown
### TC-01: Basic Volume Lifecycle

**Objective:** Verify end-to-end volume provisioning, mounting, and cleanup

**Prerequisites:**
- RDS accessible at 198.51.100.20 (management) and 198.51.100.20 (storage)
- Kubernetes cluster with RDS CSI driver deployed
- SSH access to worker node for verification
- At least 10GB free space on RDS

**Estimated Time:** 5 minutes

**Steps:**

1. Create PVC
   ```bash
   kubectl apply -f - <<EOF
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: test-validation-pvc
   spec:
     accessModes: [ReadWriteOnce]
     storageClassName: rds-nvme-tcp
     resources:
       requests:
         storage: 5Gi
   EOF
   ```

   **Expected:** PVC created, status "Pending"
   ```
   NAME                  STATUS    VOLUME   CAPACITY   ACCESS MODES   STORAGECLASS
   test-validation-pvc   Pending                                      rds-nvme-tcp
   ```

2. Wait for provisioning (10-30s expected)
   ```bash
   kubectl get pvc test-validation-pvc --watch
   ```

   **Expected:** Status changes to "Bound" within 30s
   ```
   test-validation-pvc   Bound     pvc-abc123   5Gi        RWO            rds-nvme-tcp
   ```

   **If stuck in Pending >60s:** Check controller logs
   ```bash
   kubectl logs -n kube-system -l app=rds-csi-controller -c rds-csi-plugin --tail=50
   ```

3. Verify volume on RDS
   ```bash
   ssh admin@192.0.2.10 '/disk print detail where slot~"pvc-"'
   ```

   **Expected:** Volume exists with NVMe/TCP export enabled
   ```
   slot: pvc-abc123
   file-size: 5GiB
   nvme-tcp-export: yes
   nvme-tcp-server-nqn: nqn.2000-02.com.mikrotik:pvc-abc123
   ```

4. Create pod using volume
   ```bash
   kubectl apply -f - <<EOF
   apiVersion: v1
   kind: Pod
   metadata:
     name: test-validation-pod
   spec:
     containers:
     - name: app
       image: nginx:alpine
       volumeMounts:
       - name: data
         mountPath: /data
     volumes:
     - name: data
       persistentVolumeClaim:
         claimName: test-validation-pvc
   EOF
   ```

   **Expected:** Pod reaches "Running" within 60s

5. Verify volume mounted in pod
   ```bash
   kubectl exec test-validation-pod -- df -h /data
   ```

   **Expected:** Filesystem shows ~5GB capacity
   ```
   Filesystem      Size  Used Avail Use% Mounted on
   /dev/nvme1n1    4.9G   24M  4.6G   1% /data
   ```

6. Write test data
   ```bash
   kubectl exec test-validation-pod -- sh -c 'echo "test" > /data/validation.txt && cat /data/validation.txt'
   ```

   **Expected:** "test" output, no errors

**Cleanup:**
```bash
kubectl delete pod test-validation-pod
kubectl delete pvc test-validation-pvc
# Wait 30s for cleanup
ssh admin@192.0.2.10 '/disk print detail where slot~"pvc-abc123"'
# Expected: No results (volume deleted)
```

**Success Criteria:**
- ✅ PVC bound within 30s
- ✅ Volume visible on RDS with correct size
- ✅ Pod running and volume mounted
- ✅ Data writable and readable
- ✅ Volume deleted after PVC deletion

**Troubleshooting:**
- PVC stuck Pending → Check controller logs, verify SSH connectivity
- Pod stuck ContainerCreating → Check node logs, verify NVMe/TCP connectivity
- Mount fails → SSH to node, check `dmesg` for NVMe errors
```

**Source:** Hardware validation guide patterns, adapted from existing E2E test structure

## State of the Art

| Aspect | Current State | Best Practice | Gap |
|--------|---------------|---------------|-----|
| Testing documentation | Comprehensive TESTING.md | Hardware validation section | Need HARDWARE_VALIDATION.md |
| CI/CD docs | Good ci-cd.md | Maintainability patterns | Add "how to add job" guide |
| Known limitations | Scattered in README | Dedicated limitations section | Consolidate into one place |
| Capability comparison | Not documented | Feature matrix vs peers | Need CAPABILITIES.md |
| Snapshot docs | Not documented | Full usage guide | Need docs/snapshots.md |
| Troubleshooting | Basic in README | Symptom-driven flows | Expand with decision trees |

**Recent Developments:**
- Phase 26 (snapshots) just completed, documentation pending
- v0.9.0 achieved 68.6% test coverage, should be highlighted
- Production deployment knowledge from v0.6-v0.9 not captured in docs
- Hardware validation procedures exist informally, not documented

**Architectural Context:**
- Dual-IP architecture (management vs storage) is critical but underdocumented
- NVMe/TCP timing assumptions discovered empirically, need formal documentation
- RouterOS 7.16+ required for some features, version matrix needed
- Mock RDS behavior diverges from real RDS in timing, needs explicit call-out

## Open Questions

1. **Snapshot Performance Baselines**
   - What we know: Btrfs snapshots are "fast" in theory
   - What's unclear: Actual timing on production RDS with various volume sizes
   - Recommendation: Measure during hardware validation, document actual timings

2. **Hardware Validation Test Execution Environment**
   - What we know: User has production cluster access now
   - What's unclear: Should tests run against production or dedicated test RDS?
   - Recommendation: Test on production (non-destructive tests), document clearly in prerequisites

3. **Gap Analysis Comparison Scope**
   - What we know: Should compare to AWS EBS CSI and Longhorn
   - What's unclear: Compare feature-for-feature or use-case-for-use-case?
   - Recommendation: Both - feature matrix for spec coverage, use case comparison for architectural differences

4. **CI/CD Documentation Maintenance Strategy**
   - What we know: Workflows change frequently
   - What's unclear: When to update docs vs when to just link to workflow files?
   - Recommendation: Document patterns/principles in docs, link to .github/workflows/ for specifics

## Sources

### Primary (HIGH confidence)
- [Kubernetes CSI Testing Drivers](https://kubernetes-csi.github.io/docs/testing-drivers.html) - Official CSI testing guide
- [Kubernetes CSI Functional Testing](https://kubernetes-csi.github.io/docs/functional-testing.html) - E2E test patterns
- [Kubernetes Volume Snapshots](https://kubernetes.io/docs/concepts/storage/volume-snapshots/) - Snapshot API documentation
- [AWS EBS CSI Driver Repository](https://github.com/kubernetes-sigs/aws-ebs-csi-driver) - Documentation structure reference
- [Longhorn Troubleshooting](https://longhorn.io/docs/1.9.1/troubleshoot/troubleshooting/) - Troubleshooting patterns

### Secondary (MEDIUM confidence)
- [IBM Block CSI Limitations](https://www.ibm.com/docs/en/stg-block-csi-driver/1.11.4?topic=notes-limitations) - Known limitations examples
- [Secrets Store CSI Known Limitations](https://secrets-store-csi-driver.sigs.k8s.io/known-limitations) - Limitation documentation patterns
- [GitHub Actions Best Practices](https://github.com/github/awesome-copilot/blob/main/instructions/github-actions-ci-cd-best-practices.instructions.md) - CI/CD documentation
- [IT Documentation Best Practices 2026](https://www.ninjaone.com/blog/it-documentation-best-practices/) - General documentation principles
- [Technical Documentation Best Practices](https://www.documind.chat/blog/technical-documentation-best-practices) - Structure and maintainability

### Tertiary (LOW confidence)
- Hardware validation templates (generic, not CSI-specific) - Structure patterns
- Deployment plan templates - Validation checklist ideas

### Existing Project Documentation
- `/path/to/rds-csi/README.md` - Entry point, good structure
- `/path/to/rds-csi/docs/TESTING.md` - Comprehensive test guide, missing hardware validation
- `/path/to/rds-csi/docs/architecture.md` - Excellent technical depth
- `/path/to/rds-csi/docs/ci-cd.md` - Good CI workflow documentation
- `/path/to/rds-csi/test/integration/hardware_integration_test.go` - Existing hardware test pattern
- `/path/to/rds-csi/test/e2e/lifecycle_test.go` - E2E test structure to adapt

## Metadata

**Confidence breakdown:**
- Hardware validation patterns: HIGH - NASA V&V guidelines, HIL testing practices are mature
- Testing documentation: HIGH - Kubernetes-CSI official docs are authoritative
- Capability gap analysis: HIGH - Multiple peer driver examples reviewed
- Known limitations: HIGH - Industry-standard documentation patterns from IBM/Azure/AWS
- Troubleshooting: MEDIUM - Best practices established, but RDS-specific patterns need discovery
- Snapshot documentation: MEDIUM - Kubernetes API is stable, but RDS CSI implementation is new
- CI/CD maintainability: HIGH - GitHub Actions best practices well-documented

**Research date:** 2026-02-05
**Valid until:** 90 days (stable domain - documentation best practices change slowly)

**Special Notes:**
- User has live cluster access RIGHT NOW - time-sensitive opportunity for hardware validation
- Phase 26 just completed - snapshot documentation is forward-looking, may need revision after real usage
- v0.9.0 production deployment experience provides unique validation opportunity
- Mock RDS tests pass but hardware behavior unknown - validation will discover gaps
