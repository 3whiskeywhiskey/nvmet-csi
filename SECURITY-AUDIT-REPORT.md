# Security Audit Report
## nvmet-csi driver - Comprehensive Security Review

**Audit Date:** 2025-11-06
**Auditor:** Claude (Anthropic AI)
**Codebase Version:** Milestone 4 (commit: 2ae0fb6)
**Scope:** Complete codebase review including source code, deployment manifests, and container configurations

---

## Executive Summary

This comprehensive security audit identified **15 security issues** across various severity levels. The most critical findings relate to SSH host key verification, command injection vulnerabilities, and privileged container configurations. While many issues are mitigated by the homelab/internal network deployment context, addressing these vulnerabilities is essential before considering production use or wider deployment.

**Severity Breakdown:**
- **Critical:** 2 issues
- **High:** 4 issues
- **Medium:** 7 issues
- **Low:** 2 issues

**Priority Recommendations:**
1. Implement SSH host key verification
2. Add comprehensive input validation for file paths
3. Reduce container privilege requirements
4. Implement rate limiting and resource controls
5. Add security context constraints

---

## Critical Severity Issues

### 1. SSH Host Key Verification Disabled by Default
**Severity:** CRITICAL
**CWE:** CWE-295 (Improper Certificate Validation)
**CVSS 3.1 Score:** 8.1 (High)

**Location:** `pkg/rds/ssh_client.go:84-88`

**Issue:**
The SSH client uses `ssh.InsecureIgnoreHostKey()` by default, completely bypassing SSH host key verification. This makes the connection vulnerable to Man-in-the-Middle (MITM) attacks.

```go
} else if c.insecureSkipVerify {
    hostKeyCallback = ssh.InsecureIgnoreHostKey()
    klog.Warning("INSECURE: Skipping SSH host key verification - not recommended for production")
} else {
    // Default: use InsecureIgnoreHostKey for backward compatibility
    hostKeyCallback = ssh.InsecureIgnoreHostKey()
    klog.V(4).Info("Using InsecureIgnoreHostKey (default) - configure HostKeyCallback for production security")
}
```

**Impact:**
- An attacker with network access could intercept SSH connections
- Attacker could gain access to RDS credentials and commands
- Potential for volume data manipulation or deletion
- Complete compromise of storage backend control plane

**Recommendation:**
1. Implement proper SSH host key verification using known_hosts file
2. Store RDS host key in Kubernetes Secret alongside private key
3. Make host key verification mandatory in production mode
4. Add configuration option to specify host key fingerprint
5. Log host key mismatches as security events

**Example Fix:**
```go
// Store expected host key in Secret
expectedHostKey := os.Getenv("RDS_HOST_KEY")
if expectedHostKey != "" {
    hostKeyCallback = ssh.FixedHostKey(parseHostKey(expectedHostKey))
} else {
    return nil, fmt.Errorf("RDS_HOST_KEY must be configured for production use")
}
```

---

### 2. Command Injection via File Path Parameter
**Severity:** CRITICAL
**CWE:** CWE-78 (OS Command Injection)
**CVSS 3.1 Score:** 9.1 (Critical)

**Location:** `pkg/rds/commands.go:26-32`

**Issue:**
The `FilePath` parameter in `CreateVolume` is passed directly to the SSH command without proper validation or sanitization. While slot names are validated, file paths are not.

```go
cmd := fmt.Sprintf(
    `/disk add type=file file-path=%s file-size=%s slot=%s nvme-tcp-export=yes nvme-tcp-server-port=%d nvme-tcp-server-nqn=%s`,
    opts.FilePath,    // <- NOT VALIDATED
    sizeStr,
    opts.Slot,        // <- Validated
    opts.NVMETCPPort,
    opts.NVMETCPNQN,
)
```

**Impact:**
- If an attacker can control the `volumePath` StorageClass parameter, they could inject arbitrary RouterOS commands
- Could lead to complete compromise of the RDS server
- Potential for data exfiltration, deletion, or system reconfiguration
- Privilege escalation on RDS system

**Attack Example:**
```yaml
parameters:
  volumePath: "/storage-pool/test.img; /user add name=attacker password=pwned group=full"
```

**Recommendation:**
1. Validate file paths against whitelist of allowed base paths
2. Reject paths containing shell metacharacters: `;`, `|`, `&`, `$`, `` ` ``, `(`, `)`, `<`, `>`, `\n`, `\r`
3. Use path.Clean() to normalize paths
4. Implement path traversal protection
5. Consider using RouterOS API instead of CLI for safer parameter passing

**Example Fix:**
```go
func validateFilePath(path string) error {
    // Check for shell metacharacters
    if strings.ContainsAny(path, ";|&$`()<>\n\r") {
        return fmt.Errorf("invalid characters in file path")
    }

    // Ensure path is within allowed base paths
    cleanPath := filepath.Clean(path)
    allowedPaths := []string{"/storage-pool/", "/nvme1/"}

    allowed := false
    for _, base := range allowedPaths {
        if strings.HasPrefix(cleanPath, base) {
            allowed = true
            break
        }
    }

    if !allowed {
        return fmt.Errorf("file path must be within allowed directories")
    }

    return nil
}
```

---

## High Severity Issues

### 3. NVMe Qualified Name (NQN) Injection Vulnerability
**Severity:** HIGH
**CWE:** CWE-88 (Argument Injection)
**CVSS 3.1 Score:** 7.5 (High)

**Location:** `pkg/rds/commands.go:31`, `pkg/nvme/nvme.go:84`

**Issue:**
NQN values are not validated before being used in both SSH commands and nvme-cli commands. While NQNs are generated internally, they could be manipulated through volume context parameters.

**Impact:**
- Command injection in nvme connect/disconnect operations
- Potential for connecting to malicious NVMe targets
- Node compromise through malicious nvme-cli arguments

**Recommendation:**
1. Validate NQN format: must match pattern `^nqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9.-]+:[a-z0-9._-]+$`
2. Reject NQNs containing spaces, semicolons, or other shell metacharacters
3. Verify NQN matches expected format before any operations
4. Use parameterized command execution where possible

---

### 4. Privileged Container with Excessive Host Access
**Severity:** HIGH
**CWE:** CWE-250 (Execution with Unnecessary Privileges)
**CVSS 3.1 Score:** 7.8 (High)

**Location:** `deploy/kubernetes/node.yaml:54-75`

**Issue:**
Node DaemonSet runs with `privileged: true`, `hostNetwork: true`, and mounts sensitive host directories including `/dev`, `/sys`, and `/var/lib/kubelet` with bidirectional mount propagation.

```yaml
securityContext:
  privileged: true  # Required for mount operations and NVMe access
  capabilities:
    add: ["SYS_ADMIN"]  # Required for mount syscalls
  allowPrivilegeEscalation: true
```

**Impact:**
- Container escape could lead to full node compromise
- Unrestricted access to host resources
- Ability to manipulate other containers on the node
- Potential for lateral movement within cluster

**Recommendation:**
1. Remove `privileged: true` if possible
2. Use specific capabilities instead of SYS_ADMIN where feasible
3. Limit mounted host paths to only what's necessary
4. Consider using CSI node driver with less privilege via CSI proxy
5. Implement AppArmor or SELinux profiles
6. Add seccomp profile to restrict syscalls

**Example Improved Security Context:**
```yaml
securityContext:
  privileged: false
  capabilities:
    add:
      - SYS_ADMIN  # For mount operations
      - CAP_NET_ADMIN  # For NVMe/TCP
    drop:
      - ALL
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  runAsUser: 65534
  seccompProfile:
    type: RuntimeDefault
```

---

### 5. Unvalidated Mount Options
**Severity:** HIGH
**CWE:** CWE-269 (Improper Privilege Management)
**CVSS 3.1 Score:** 7.3 (High)

**Location:** `pkg/mount/mount.go:84-86`, `pkg/driver/node.go:223-231`

**Issue:**
Mount options provided by users through VolumeCapability are passed directly to the mount command without validation. Malicious mount options could lead to privilege escalation.

```go
// Add mount options if specified
if len(options) > 0 {
    args = append(args, "-o", strings.Join(options, ","))
}
```

**Impact:**
- Users could specify dangerous mount options like `exec`, `suid`, `dev`
- Privilege escalation through suid binaries on mounted volumes
- Container escape through device nodes

**Recommendation:**
1. Implement mount option whitelist
2. Reject dangerous options: `suid`, `dev`, `exec` (unless explicitly allowed)
3. Enforce `nosuid,nodev,noexec` by default
4. Log all mount operations with full options

**Example Fix:**
```go
func validateMountOptions(options []string) error {
    dangerousOptions := []string{"suid", "dev", "exec"}
    for _, opt := range options {
        for _, dangerous := range dangerousOptions {
            if opt == dangerous || strings.HasPrefix(opt, dangerous+",") {
                return fmt.Errorf("mount option %s is not allowed", dangerous)
            }
        }
    }
    return nil
}
```

---

### 6. Missing Input Validation for Volume Context Parameters
**Severity:** HIGH
**CWE:** CWE-20 (Improper Input Validation)
**CVSS 3.1 Score:** 7.5 (High)

**Location:** `pkg/driver/node.go:74-88`

**Issue:**
Volume context parameters (nvmeAddress, nvmePort, nqn) from PV are used without validation. These could be manipulated if an attacker gains access to create/modify PVs.

**Impact:**
- Users could specify arbitrary NVMe targets
- Connection to malicious NVMe targets could compromise node
- Port specification could be used for port scanning
- Potential for SSRF (Server-Side Request Forgery) attacks

**Recommendation:**
1. Validate nvmeAddress is a valid IP address
2. Validate nvmePort is in valid range (1-65535) and not a privileged port
3. Verify nvmeAddress matches expected RDS server address
4. Validate NQN format
5. Implement allowlist of trusted NVMe target addresses

---

## Medium Severity Issues

### 7. No Rate Limiting on SSH Connections
**Severity:** MEDIUM
**CWE:** CWE-770 (Allocation of Resources Without Limits)
**CVSS 3.1 Score:** 5.3 (Medium)

**Location:** `pkg/rds/ssh_client.go:72-121`

**Issue:**
No rate limiting, connection pooling, or maximum concurrent connection limits for SSH connections to RDS.

**Impact:**
- Potential DoS against RDS server through connection exhaustion
- Resource exhaustion on controller pod
- RDS could become unresponsive during volume provisioning storms

**Recommendation:**
1. Implement connection pooling with maximum pool size
2. Add rate limiting for volume operations
3. Implement circuit breaker pattern for SSH connection failures
4. Add timeout for all SSH operations
5. Use single persistent connection with multiplexing instead of per-operation connections

---

### 8. Insufficient Error Message Sanitization
**Severity:** MEDIUM
**CWE:** CWE-209 (Generation of Error Message Containing Sensitive Information)
**CVSS 3.1 Score:** 5.3 (Medium)

**Location:** Multiple files including `pkg/rds/commands.go`, `pkg/driver/controller.go`

**Issue:**
Error messages include detailed system information, command output, and internal paths that could aid attackers.

**Examples:**
```go
return "", fmt.Errorf("failed to connect to %s: %w", addr, err)
return "", fmt.Errorf("nvme connect failed: %w, output: %s", err, string(output))
```

**Impact:**
- Information disclosure about internal system configuration
- Reveals internal IP addresses and paths
- Assists attackers in reconnaissance
- Could expose sensitive command output

**Recommendation:**
1. Sanitize error messages for external users
2. Log detailed errors internally but return generic messages to users
3. Remove internal paths and IPs from user-facing errors
4. Implement error classification (internal vs. user-facing)

---

### 9. No Timeout on NVMe/TCP Operations
**Severity:** MEDIUM
**CWE:** CWE-400 (Uncontrolled Resource Consumption)
**CVSS 3.1 Score:** 5.3 (Medium)

**Location:** `pkg/nvme/nvme.go:93-96`

**Issue:**
NVMe connect operations don't have explicit timeouts, could hang indefinitely.

**Impact:**
- Pod stuck in ContainerCreating state
- Resource exhaustion from hung goroutines
- DoS through volume provisioning

**Recommendation:**
1. Add context with timeout to all nvme-cli operations
2. Implement configurable timeout for device appearance
3. Add healthcheck for stuck volume operations
4. Implement automatic cleanup of hung operations

---

### 10. Regex Denial of Service (ReDoS) Vulnerability
**Severity:** MEDIUM
**CWE:** CWE-1333 (Inefficient Regular Expression Complexity)
**CVSS 3.1 Score:** 5.3 (Medium)

**Location:** `pkg/rds/commands.go:181-254`

**Issue:**
Complex regex patterns for parsing RouterOS output could be vulnerable to ReDoS attacks with crafted input.

**Example:**
```go
if match := regexp.MustCompile(`file-size=([\d.]+)\s*([KMGT]i?B)`).FindStringSubmatch(normalized); len(match) > 2 {
```

**Impact:**
- CPU exhaustion from malicious RouterOS output
- DoS of controller pod
- Delayed volume operations

**Recommendation:**
1. Use simple string parsing instead of complex regex where possible
2. Set timeout for regex operations
3. Validate input length before regex matching
4. Use non-backtracking regex engines where available
5. Implement input sanitization before regex matching

---

### 11. Insufficient Logging of Security Events
**Severity:** MEDIUM
**CWE:** CWE-778 (Insufficient Logging)
**CVSS 3.1 Score:** 4.3 (Medium)

**Location:** Throughout codebase

**Issue:**
Security-relevant events (authentication failures, validation failures, unusual operations) are not consistently logged or logged at appropriate severity levels.

**Impact:**
- Difficult to detect attacks or security incidents
- No audit trail for compliance
- Delayed incident response

**Recommendation:**
1. Implement structured logging with security event classification
2. Log all authentication attempts (success and failure)
3. Log all volume operations with user/pod identity
4. Add metrics for security events
5. Implement log aggregation and monitoring

---

### 12. Container Image Not Signed or Verified
**Severity:** MEDIUM
**CWE:** CWE-494 (Download of Code Without Integrity Check)
**CVSS 3.1 Score:** 5.9 (Medium)

**Location:** `deploy/kubernetes/controller.yaml:69`, `deploy/kubernetes/node.yaml:39`

**Issue:**
Container images are pulled without signature verification. No evidence of image signing in CI/CD.

**Impact:**
- Supply chain attack through compromised images
- Malicious code execution in cluster
- Unauthorized access to credentials and cluster resources

**Recommendation:**
1. Sign container images with cosign or similar tool
2. Implement image signature verification in deployment
3. Use image digests instead of tags
4. Enable image pull policies with verification
5. Implement vulnerability scanning in CI/CD

---

### 13. Missing Secrets Encryption at Rest
**Severity:** MEDIUM
**CWE:** CWE-311 (Missing Encryption of Sensitive Data)
**CVSS 3.1 Score:** 6.5 (Medium)

**Location:** `deploy/kubernetes/controller.yaml:5-20`

**Issue:**
Kubernetes Secrets containing SSH private key are not encrypted at rest unless cluster-wide encryption is enabled.

**Impact:**
- SSH private key accessible to anyone with etcd access
- Potential for credential theft from backups
- Compliance issues

**Recommendation:**
1. Enable Kubernetes encryption at rest for Secrets
2. Document encryption requirements in deployment guide
3. Consider using external secret management (Vault, AWS Secrets Manager)
4. Implement secret rotation procedures
5. Use short-lived credentials where possible

---

### 14. No Resource Limits on Command Output
**Severity:** MEDIUM
**CWE:** CWE-400 (Uncontrolled Resource Consumption)
**CVSS 3.1 Score:** 4.3 (Medium)

**Location:** `pkg/rds/ssh_client.go:163-180`

**Issue:**
SSH command output is buffered in memory without size limits. Malicious or malformed RouterOS output could cause memory exhaustion.

**Impact:**
- OOM kills of controller pod
- DoS of volume provisioning
- Cluster instability

**Recommendation:**
1. Implement maximum output size limit (e.g., 1MB)
2. Use streaming instead of buffering for large outputs
3. Add memory limits to container specifications
4. Implement output size monitoring

---

## Low Severity Issues

### 15. Weak Default Filesystem Permissions
**Severity:** LOW
**CWE:** CWE-732 (Incorrect Permission Assignment)
**CVSS 3.1 Score:** 3.3 (Low)

**Location:** `pkg/mount/mount.go:71`

**Issue:**
Target directories created with `0750` permissions, which may be more permissive than necessary.

**Recommendation:**
Consider using `0700` for staging directories unless broader access is required.

---

### 16. No Security Headers in Logging
**Severity:** LOW
**CWE:** CWE-532 (Insertion of Sensitive Information into Log File)
**CVSS 3.1 Score:** 3.3 (Low)

**Location:** Throughout codebase (klog usage)

**Issue:**
Logs may contain sensitive information (volume IDs, paths, IPs) without classification or redaction.

**Recommendation:**
1. Implement log sanitization for sensitive data
2. Add security classification to log entries
3. Avoid logging full command outputs at INFO level
4. Implement log retention and access controls

---

## Positive Security Findings

Despite the identified issues, the codebase demonstrates several good security practices:

1. **Input Validation for Volume IDs:** Strong validation using regex to prevent injection (`pkg/utils/volumeid.go:43-53`)
2. **Slot Name Validation:** Proper validation preventing command injection in slot names (`pkg/rds/commands.go:343-355`)
3. **Least Privilege RBAC:** Controller and node have separate service accounts with appropriate permissions
4. **Secrets Management:** SSH keys stored in Kubernetes Secrets (though could be improved)
5. **Read-Only Secrets Mount:** Secrets mounted read-only in controller (`deploy/kubernetes/controller.yaml:112`)
6. **Multi-Stage Docker Build:** Minimal runtime image reduces attack surface
7. **Idempotent Operations:** Volume operations are idempotent, reducing risk of state corruption
8. **Use of Standard Libraries:** Uses well-maintained SSH and crypto libraries

---

## Remediation Priority

### Immediate (Fix within 1 week)
1. **Issue #1:** Implement SSH host key verification
2. **Issue #2:** Add file path validation and sanitization
3. **Issue #5:** Validate and whitelist mount options

### Short-term (Fix within 1 month)
4. **Issue #3:** Validate NQN format
5. **Issue #4:** Reduce container privileges
6. **Issue #6:** Validate volume context parameters
7. **Issue #7:** Implement rate limiting
8. **Issue #12:** Sign and verify container images

### Medium-term (Fix within 3 months)
9. **Issue #8:** Sanitize error messages
10. **Issue #9:** Add timeouts to NVMe operations
11. **Issue #11:** Enhance security logging
12. **Issue #13:** Document encryption requirements

### Long-term (Fix within 6 months)
13. **Issue #10:** Optimize regex patterns
14. **Issue #14:** Implement output size limits
15. **Issue #15-16:** Minor permission and logging improvements

---

## Testing Recommendations

1. **Penetration Testing:** Conduct penetration testing focusing on:
   - Command injection attempts
   - MITM attacks on SSH connections
   - Container escape attempts
   - RBAC bypass attempts

2. **Fuzzing:** Implement fuzzing for:
   - RouterOS command parsing
   - Volume ID validation
   - NQN parsing
   - Mount option parsing

3. **Security Scanning:**
   - Regular vulnerability scanning with Trivy or Grype
   - Static analysis with gosec
   - SAST scanning in CI/CD pipeline
   - Dependency vulnerability scanning

4. **Chaos Engineering:**
   - Test behavior under SSH connection failures
   - Test behavior with malformed RouterOS output
   - Test resource exhaustion scenarios

---

## Compliance Considerations

For production use or compliance-sensitive environments, consider:

1. **SOC 2 Compliance:**
   - Implement comprehensive logging and monitoring
   - Add encryption at rest and in transit
   - Document security controls

2. **PCI DSS:**
   - If storing payment card data on volumes, ensure encryption
   - Implement access controls and logging
   - Regular security testing

3. **HIPAA:**
   - Encryption of data at rest and in transit
   - Audit logging of all access
   - Access controls and authentication

4. **GDPR:**
   - Data protection by design
   - Ability to delete data (volumes)
   - Logging and audit trails

---

## Conclusion

The nvmet-csi driver is suitable for homelab and internal network use cases in its current state, but requires significant security hardening before production deployment or use in untrusted environments.

The most critical issues (SSH MITM vulnerability and command injection) should be addressed immediately. The privileged container configuration, while necessary for CSI functionality, represents a significant risk surface that should be minimized through careful capability management and security policies.

With proper remediation of the identified issues, the driver can achieve a production-ready security posture suitable for enterprise deployment.

---

## Appendix A: Security Tools Recommendations

1. **Static Analysis:**
   - gosec (Go security checker)
   - semgrep with custom rules
   - golangci-lint with security linters

2. **Dynamic Analysis:**
   - CSI sanity tests with security focus
   - Penetration testing framework
   - Chaos engineering tools

3. **Container Security:**
   - Trivy for vulnerability scanning
   - Falco for runtime security monitoring
   - OPA/Gatekeeper for policy enforcement

4. **Secrets Management:**
   - HashiCorp Vault
   - AWS Secrets Manager
   - Sealed Secrets for GitOps

---

## Appendix B: Security Checklist for Production Deployment

- [ ] SSH host key verification enabled
- [ ] All input validation implemented
- [ ] Container privileges minimized
- [ ] Rate limiting configured
- [ ] Security logging enabled
- [ ] Container images signed
- [ ] Secrets encrypted at rest
- [ ] Network policies configured
- [ ] RBAC reviewed and minimized
- [ ] Security monitoring enabled
- [ ] Incident response plan documented
- [ ] Backup and disaster recovery tested
- [ ] Security testing completed
- [ ] Compliance requirements verified
- [ ] Documentation reviewed and updated

---

**Report End**

For questions or clarifications about this security audit, please create an issue in the repository.
