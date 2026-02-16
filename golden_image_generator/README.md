# Golden Image Generator (GIG)

GIG is a Packer enabled Virtual Machine image generator for Crane-OSS.
It's purpose is to store the image all newly provisioned Crane hosts should have. Most importantly it will create a VM that already
contains subd - which is the program that would then communicate with Crane in order to inform it of any issues with the Host.

## Usage
Run at project root:
```
packer validate -var-file=aws-credentials.auto.pkrvars.hcl ./templates/aws-ami.pkr.hcl
```
where the file `aws-credentials.auto.pkrvars.hcl` contains your AWS access key and secret key like this:
```
aws_access_key = "..."
aws_secret_key = "..."
```