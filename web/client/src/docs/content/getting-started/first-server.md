# Launch Your First Server

This walkthrough takes you from an empty account to a running virtual machine. It assumes you've already [activated billing](/docs/getting-started/account-setup) — without an active profile the create buttons stay disabled.

## Step 1 — Have a project to work in

You're always inside a project, and your first sign-in gave you a default one, so you can start there. If you'd rather keep things separate, create a fresh one:

1. Open the project switcher at the top of the sidebar and choose to create a new project (or go to **Organization → Projects**).
2. Give it a name that tells you what it holds — `demo`, `staging`, a client name.
3. The new project becomes your active context; the sidebar now scopes to it.

Projects also anchor teamwork and billing: members are added per project, and costs roll up per project. To pull colleagues in, see [Teammates and Invitations](/docs/guides/team-members).

## Step 2 — Line up what the server needs

A server is assembled from a few smaller pieces. You can create them as you go through the launch form, but it helps to know what they are:

- **Image** — the operating system the machine boots from. Browse **Compute → Images** to see what's available.
- **Flavor** — the size (vCPUs, RAM, root disk). You pick this in the launch form.
- **Network** — the private network the server attaches to. If the project has none yet, create one under **Network → Networks**.
- **Key pair** — the SSH key you'll log in with. Under **Compute → Key pairs**, upload a public key you already have or let the portal generate one and download the private half. You can't retrieve a generated private key later, so save it immediately.
- **Security group** — the firewall around the server. A default group usually exists; open **Network → Security groups** to allow the ports you need (for example SSH on 22).

## Step 3 — Launch the server

1. Go to **Compute → Servers** and start a new server.
2. Name it, then choose the **image** and **flavor**.
3. Attach it to a **network** and select the **key pair** from Step 2.
4. Assign a **security group** that opens the ports you'll use.
5. Leave **Assign floating IP** on (the default) to have a public address attached automatically shortly after the server comes up.
6. Confirm, and Stratos provisions the machine on the underlying cloud. It moves through build states and settles on **ACTIVE** once it's up. (For what's happening behind that, see [How Provisioning Works](/docs/concepts/provisioning).)

## Step 4 — Reach it from the internet

If you left **Assign floating IP** on in the launch form, the server picks up a public address on its own shortly after going **ACTIVE** — skip to the SSH step. To attach one by hand instead:

1. Under **Network → Floating IPs**, allocate a floating IP.
2. Associate that IP with your server.
3. SSH in with the private key from your key pair — for example `ssh -i mykey.pem <user>@<floating-ip>`. The default login user depends on the image you chose.

## Step 5 — Operate and tidy up

From the server's page you can run power actions — soft or hard reboot, stop, start — and, when you're finished, delete it. Deleting releases the compute so it stops accruing charges; release the floating IP too if you no longer need it.

You can drive these same actions from an AI assistant instead of the console — see [AI Agent Access](/docs/guides/ai-agents).

That's the full loop: project, building blocks, launch, connect, operate. The [task guides](/docs/guides/servers) go deeper on each resource type.
