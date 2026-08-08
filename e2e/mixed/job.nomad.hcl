# Mixed-backend e2e: a single secret block resolves 1Password (op://), AWS
# Parameter Store (aws-ssm:), and AWS Secrets Manager (aws-sm:) references at
# once. Both backends are configured on the node; every reference names its
# scheme (scheme-less would be ambiguous with >1 backend).
job "e2e-mixed-secrets" {
  type = "batch"

  group "g" {
    reschedule {
      attempts  = 0
      unlimited = false
    }
    restart {
      attempts = 0
      mode     = "fail"
    }

    task "print" {
      driver = "raw_exec"

      secret "mix" {
        provider = "remote-secrets"
        path     = <<-EOF
          op_pw    = op://Testing/database/password
          ssm_pw   = aws-ssm:/prod/db/password
          sm_plain = aws-sm:prod/sm/plain
          sm_creds = aws-sm:prod/sm/creds
        EOF
      }

      env {
        OP_PW   = "${secret.mix.op_pw}"
        SSM_PW  = "${secret.mix.ssm_pw}"
        SM_PW   = "${secret.mix.sm_plain}"
        SM_USER = "${secret.mix.sm_creds_username}"
      }

      config {
        command = "/bin/sh"
        args    = ["-c", "echo \"OP=$${OP_PW} SSM=$${SSM_PW} SM=$${SM_PW} SMUSER=$${SM_USER}\""]
      }
    }
  }
}
