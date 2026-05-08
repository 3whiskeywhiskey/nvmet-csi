# Security Hardening Guide

This document describes the security hardening measures implemented in the nvmet-csi driver.

## Container Security

### Node Plugin Security Context

The node plugin DaemonSet has been hardened with the following security measures:

#### Capabilities
- **Minimal Capabilities**: Only `CAP_SYS_ADMIN` and `CAP_NET_ADMIN` are granted
  - `CAP_SYS_ADMIN`: Required for mount/umount syscalls
  - `CAP_NET_ADMIN`: Required for NVMe/TCP network operations
  - All other capabilities are explicitly dropped

#### Privilege Restrictions
- **privileged: false**: Container does not run in privileged mode
- **allowPrivilegeEscalation: false**: Prevents gaining additional privileges
- **readOnlyRootFilesystem: true**: Root filesystem is read-only
- **Writable Volumes**: Only `/tmp` and `/var/run` are writable (emptyDir)

#### User Context
- **runAsUser: 0**: Required for mount operations (root user)
- Note: CSI node plugins typically require root for mount/umount syscalls

#### SELinux
- **type: spc_t**: Super Privileged Container type for SELinux

#### Seccomp
- **seccompProfile: RuntimeDefault**: Syscall filtering using runtime default profile

### Testing Requirements

**IMPORTANT**: These security changes require thorough testing on actual hardware before production deployment.

#### Test Checklist

- [ ] Test volume creation (PVC → PV binding)
- [ ] Test NVMe/TCP connection establishment
- [ ] Test volume mounting to pod
- [ ] Test volume unmounting from pod
- [ ] Test volume deletion
- [ ] Test with different filesystems (ext4, xfs)
- [ ] Test with SELinux enabled
- [ ] Test with AppArmor enabled (if applicable)
- [ ] Test pod restarts
- [ ] Test node restarts
- [ ] Verify no privilege escalation warnings in logs
- [ ] Verify mount operations succeed
- [ ] Verify NVMe device discovery works
- [ ] Check for permission denied errors

#### Known Limitations

1. **Root User Required**: CSI node plugins need root for mount operations
   - `runAsNonRoot: true` is not compatible with mount requirements
   - This is standard for CSI drivers that perform mount operations

2. **Host Network Required**: NVMe/TCP connections require host network access
   - This is necessary for direct NVMe/TCP communication with RDS

3. **Device Access**: Requires access to `/dev` and `/sys` for NVMe device discovery

#### Troubleshooting

If the node plugin fails to start or mount operations fail:

1. **Check pod logs**:
   ```bash
   kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin
   ```

2. **Check for permission errors**:
   ```bash
   kubectl describe pod -n kube-system -l app=nvmet-csi-node
   ```

3. **Verify capabilities**:
   ```bash
   # Inside the container
   capsh --print
   ```

4. **Check seccomp profile**:
   ```bash
   kubectl get pod -n kube-system <pod-name> -o json | jq '.spec.securityContext.seccompProfile'
   ```

5. **Test mount operations manually**:
   ```bash
   kubectl exec -n kube-system <pod-name> -c nvmet-csi-plugin -- mount
   ```

#### Fallback Option

If issues arise in production and require urgent resolution, you can temporarily revert to less restrictive settings:

**NOT RECOMMENDED FOR PRODUCTION** - Only for emergency troubleshooting:

```yaml
securityContext:
  privileged: true
  allowPrivilegeEscalation: true
  readOnlyRootFilesystem: false
```

After resolving issues, restore the hardened security settings.

## Additional Security Measures

### Input Validation
- File paths validated against whitelist
- NQN format strictly validated
- Shell metacharacters rejected
- Volume IDs validated for format

### SSH Security
- Host key verification enforced
- No InsecureIgnoreHostKey in production
- Private keys stored in Kubernetes Secrets with restricted permissions

### Network Security
- Control plane (SSH): Port 22, separate VLAN recommended
- Data plane (NVMe/TCP): Port 4420, separate VLAN recommended
- Network policies should restrict access to RDS endpoints

### Secrets Management
- SSH private keys in Kubernetes Secrets
- Secrets should use encryption at rest
- Consider using external secret management (Vault, etc.)

## Security Audit Compliance

These hardening measures address:
- **Fix #4**: Reduce Container Privileges (High Priority)
- Principle of least privilege
- Defense in depth
- Minimal attack surface

## References

- [Kubernetes Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
- [Container Security Best Practices](https://kubernetes.io/docs/concepts/security/security-checklist/)
- [CSI Driver Security](https://kubernetes-csi.github.io/docs/security.html)
- [Linux Capabilities](https://man7.org/linux/man-pages/man7/capabilities.7.html)
- [Seccomp](https://kubernetes.io/docs/tutorials/security/seccomp/)
