# Setting Up SPIRE-Server on AWS EC2 with RDS PostgreSQL

This guide covers setting up SPIRE-Server on an AWS EC2 instance with a PostgreSQL database on AWS RDS.

## Prerequisites

- AWS Account
- SSH key pair for EC2 access
- GHCR (GitHub Container Registry) account for pulling SPIRE images

---

## Part 1: Create PostgreSQL Database on AWS RDS

### 1.1 Create RDS PostgreSQL Instance

1. **Navigate to AWS Console → RDS → Create database**

2. **Choose database creation method:**
   - Select: "Standard create"
   - Engine options: PostgreSQL
   - Version: Select latest available (e.g., PostgreSQL 17.6)

3. **Configure instance:**
   - Instance class: `db.t4g.micro` (or `db.t3.micro` for free tier)
   - Multi-AZ deployment: No (for cost savings)
   - Storage type: General Purpose SSD
   - Allocated storage: 20 GB (minimum)

4. **Configure settings:**
   - Master username: `postgres`
   - Master password: Set a strong password (remember this for later)
   - Confirm password: Same as above

5. **Configure connectivity:**
   - Virtual Private Cloud (VPC): Default VPC
   - Subnet group: Default
   - Public access: **No** (we'll connect from EC2 in the same VPC)
   - VPC security group: Create new or select existing

6. **Database authentication:**
   - Select: "Password authentication"

7. **Additional configuration:**
   - Initial database name: `crane` (or `spire` - will create later)
   - Port: 5432 (default)

8. **Click "Create database"**

### 1.2 Configure RDS Security Group

1. **Go to AWS Console → RDS → Databases → Your instance**

2. **Click on the security group link (under "Security")**

3. **Edit inbound rules → Add rule:**
   - Type: PostgreSQL
   - Port: 5432
   - Source: Your EC2 security group (sg-xxxxxxxx)
   - Description: Allow PostgreSQL from EC2

4. **Save rules**

### 1.3 Verify RDS Connection

From your local machine (or anywhere with PostgreSQL client):

```bash
psql 'host=your-rds-endpoint.amazonaws.com port=5432 user=postgres dbname=postgres sslrootcert=/certs/global-bundle.pem sslmode=verify-full'
```

Or use a GUI tool like DBeaver or pgAdmin.

---

## Part 2: Launch EC2 Instance

### 2.1 Launch EC2 Instance

1. **Go to AWS Console → EC2 → Launch instance**

2. **Configure instance:**
   - Name: `spire-server`
   - Amazon Machine Image: Amazon Linux 2 (or Ubuntu 24.04 LTS)
   - Instance type: `t4g.micro` (free tier eligible, ARM) or `t3.micro` (x86)
   - Key pair: Select your existing key pair or create new

3. **Network settings:**
   - VPC: Default
   - Subnet: Any public subnet
   - Auto-assign public IP: Enable
   - Firewall (Security Group): Create new security group
     - Add rules:
       - SSH (port 22): Your IP only
       - Custom TCP (port 44000): Your IP (for dominator)
       - Custom TCP (port 8081): Your IP (for spire-server)

4. **Storage:**
   - Root volume: 8 GB (default)

5. **Click "Launch instance"**

### 2.2 Create IAM Role for ECR Access (Optional - for pulling images)

1. **Go to AWS Console → IAM → Roles → Create role**

2. **Select trusted entity:**
   - AWS service: EC2

3. **Add permissions:**
   - Search and attach: `AmazonEC2ContainerRegistryReadOnly`

4. **Role name:** `EC2ECRReadOnlyRole`

5. **Attach role to EC2:**
   - Go to EC2 → Your instance → Actions → Security → Modify IAM role
   - Select the role → Update IAM role

---

## Part 3: Connect to EC2 and Install Docker

### 3.1 SSH into EC2 Instance

```bash
ssh -i your-key.pem ec2-user@your-ec2-public-ip
```

### 3.2 Install Docker (Amazon Linux 2)

```bash
sudo yum update -y
sudo yum install -y docker
sudo systemctl start docker
sudo systemctl enable docker
sudo usermod -a -G docker ec2-user
```

**Important:** Log out and log back in, or run:
```bash
newgrp docker
```

Verify Docker works:
```bash
docker ps
```

### 3.3 Install Docker Compose (Optional)

```bash
sudo curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose
sudo chmod +x /usr/local/bin/docker-compose
docker-compose --version
```

---

## Part 4: Pull SPIRE-Server Image from GHCR

### 4.1 Pull SPIRE-Server Image

```bash
docker pull ghcr.io/spiffe/spire-server:nightly
```

---

## Part 5: Configure SPIRE-Server

### 5.1 Create Directories on EC2

```bash
mkdir -p ~/spire/server ~/spire/data
```

### 5.2 Create server.conf

Create a file at `~/spire/server/server.conf` with the following content:

```conf
server {
    bind_address = "0.0.0.0"
    bind_port = "8081"
    trust_domain = "crane.internal"
    data_dir = "/opt/spire/data"
}

plugins {
    DataStore "sql" {
        plugin_data {
            database_type = "postgres"
            connection_string = "host=YOUR_RDS_ENDPOINT.us-west-2.rds.amazonaws.com port=5432 dbname=spire user=postgres password=YOUR_PASSWORD sslmode=require"
        }
    }
    
    NodeAttestor "aws_iid" {
        plugin_data {
            account_id_whitelist = ["YOUR_AWS_ACCOUNT_ID"]
        }
    }
    
    KeyManager "disk" {
        plugin_data {
            keys_path = "/opt/spire/data/keys"
        }
    }
}
```

**Replace:**
- `YOUR_RDS_ENDPOINT` - Your RDS instance endpoint 
- `YOUR_PASSWORD` - Your PostgreSQL master password
- `YOUR_AWS_ACCOUNT_ID` - Your AWS account ID

### 5.3 Create the Spire Database

If you didn't create it during RDS setup:

```bash
docker run -it --rm postgres:15 \
  psql -h YOUR_RDS_ENDPOINT.us-west-2.rds.amazonaws.com \
  -p 5432 -U postgres -c "CREATE DATABASE spire;"
```

---

## Part 6: Run SPIRE-Server Container

### 6.1 Run the Container

```bash
docker run -d \
  --name crane-spire-server \
  -p 8081:8081 \
  -v ~/spire/server:/opt/spire/conf/server \
  -v ~/spire/data:/opt/spire/data \
  ghcr.io/spiffe/spire-server:nightly
```

### 6.2 Verify Container is Running

```bash
docker ps
```

### 6.3 Check Logs

```bash
docker logs crane-spire-server
```

You should see logs indicating:
- Database connection established
- Server listening on port 8081
- CA prepared and activated

### 6.4 Health Check

From within the container:
```bash
docker exec crane-spire-server /opt/spire/bin/spire-server healthcheck
```

From outside (after opening port 8081):
```bash
nc -zv your-ec2-ip 8081
```

---

## Part 7: Open Ports in Security Group (If Not Already Done)

### 7.1 Open Port 8081

**Via AWS Console:**
1. Go to EC2 → Security Groups
2. Select the security group attached to your EC2 instance
3. Edit inbound rules → Add rule:
   - Type: Custom TCP
   - Port: 8081
   - Source: Your IP (or 0.0.0.0/0 for anywhere)
4. Save

**Via AWS CLI:**
```bash
aws ec2 authorize-security-group-ingress \
  --group-id sg-xxxxxxxx \
  --protocol tcp \
  --port 8081 \
  --cidr your-ip/32
```

Find your security group ID:
```bash
aws ec2 describe-instances --instance-id i-xxxxxxxx \
  --query 'Reservations[0].Instances[0].SecurityGroups[0].GroupId' \
  --output text
```

---

## Troubleshooting

### Docker Permission Denied Error

```
permission denied while trying to connect to the Docker daemon socket
```

**Fix:**
```bash
newgrp docker
```
Or log out and log back in.

### No Basic Auth Credentials

```
Error response from daemon: Head "https://.../manifests/latest": no basic auth credentials
```

**Fix:** Don't use `sudo docker` - use `docker` without sudo after adding user to docker group.

### RDS Connection Timeout

```
psql: error: connection to server at "...", port 5432 failed: Connection timed out
```

**Fix:** Ensure RDS security group allows inbound PostgreSQL (port 5432) from EC2's security group.

### PostgreSQL Authentication Error

```
no pg_hba.conf entry for host "...", user "postgres", database "spire", no encryption
```

**Fix:** In server.conf, change `sslmode=disable` to `sslmode=require`.

---

## Cost Estimates

| Service | Instance Type | Approximate Cost |
|---------|---------------|------------------|
| EC2 | t4g.micro | ~$8-10/month |
| RDS | db.t4g.micro | ~$10/month (after free tier) |
| ECR Storage | Varies | ~$0.10/GB/month |
| Data Transfer | Varies | ~$0.09/GB |

**Total estimated cost: ~$18-20/month**

---

## Additional Notes

### Removing Hardcoded Credentials

In production, avoid hardcoding AWS credentials in docker-compose.yml. Use:
- IAM roles for EC2
- AWS Secrets Manager for sensitive data