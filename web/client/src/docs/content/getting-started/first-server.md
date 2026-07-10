# Launch Your First Server

This walkthrough takes you from an empty project to a running virtual machine you can log into — and then safely tears it back down. It assumes you've already [signed in and activated billing](/docs/getting-started/account-setup); until a billing profile is active, the **Create** buttons stay disabled.

Here's the whole loop: pick a project, make an SSH key, fill the launch form, watch the server boot, connect, and clean up. Ten minutes, start to finish.

## Step 1 — Pick a project to work in

Everything you create lives inside a **project** — it scopes your servers, networks, teammates and billing. Your first sign-in created a default project, so you can start there. To keep work separate, open the **project switcher** at the top of the sidebar and create a new one (name it for what it holds — `demo`, `staging`, a client name). Whatever the switcher shows is your active context; every resource below lands in that project.

New to projects and organizations? See [Meet the Stratos Portal](/docs/getting-started/overview).

## Step 2 — Create an SSH key pair

A key pair is how you log into a Linux server without a password. Make it before you launch so it's ready to attach.

1. Go to **Compute → Key pairs** and choose **Create keypair**.
2. Give it a name (for example `my-first-key`). If you already have an SSH key, paste its **public** half (`ssh-ed25519 …` or `ssh-rsa …`) into **Public key**. To have one generated for you, leave that field empty.

![The Create keypair dialog](/docs-img/first-server-keypair-create.png)

3. If you let the cloud generate the key, the **private key is shown exactly once**. Download the `.pem` (or copy it) and store it somewhere safe — it is never shown again, and without it you can't SSH in.

![The private key is shown only once — download it now](/docs-img/first-server-keypair-private.png)

> Lock the private key down before using it — on macOS/Linux run `chmod 600 my-first-key.pem`. If you ever lose it, delete the key pair and create a new one.

## Step 3 — Open the launch form

Head to **Compute → Servers** — this is where your virtual machines live. Choose **Create server** in the top-right.

![The Servers page with the Create server button](/docs-img/first-server-servers-header.png)

The launch form is one page of numbered steps. Work down it top to bottom.

## Step 4 — Choose location, image and flavor

**Location & availability zone (steps 1–2).** Pick the region your server runs in. If only one is offered it's already selected; leave the availability zone on its default unless you have a reason to pin one.

**Image (step 3).** The image is the operating system the server boots from. For a first server, pick **Ubuntu Server 24.04 LTS** — it's small, widely documented, and every example below assumes it.

![Selecting the Ubuntu Server 24.04 image](/docs-img/first-server-create-image.png)

**Flavor (step 4).** The flavor is the hardware size — vCPUs, RAM and root disk — grouped into families (GPU, general purpose, compute, memory, burstable). A first server doesn't need much: a small burstable size like **t3.small** (2 vCPU / 2 GB) is plenty and inexpensive. You can resize later.

![Selecting the t3.small flavor](/docs-img/first-server-create-flavor.png)

## Step 5 — Attach a network and a public IP

**Network (step 5).** Check the private network your server should join. Most projects already have one; if the list is empty, create a network under **Network → Networks** first, then come back. Leave **Fixed IP** blank to let the network assign one automatically.

![Selecting the project network](/docs-img/first-server-create-network.png)

**Public IP (step 6).** Leave **Assign floating IP** on (the default) so the server picks up a public address on its own shortly after it boots — that's the address you'll SSH to. You can also attach one by hand later from **Network → Floating IPs**.

## Step 6 — Set up access

Open **step 7, Access**. There are two ways to log in:

- **SSH key pair (recommended)** — choose the key pair you made in Step 2. Most secure, and no password to manage.
- **Password** — set a **username** and **password**. With a username, the portal creates a sudo login user for you via cloud-init (works on any Ubuntu image). The screenshot below uses this method.

Ports are controlled by **Security groups**. To reach the server over SSH it needs a group that opens **port 22** — the `default` group or an `allow-all` group both work while you're getting started. **User data** is optional: paste a cloud-init script to run on first boot (install packages, create users), or leave it blank.

![Choosing a login method and a security group that allows SSH](/docs-img/first-server-create-access.png)

## Step 7 — Name it and launch

Give the server a name in **step 8** (for example `my-first-server`), then choose **Create server**.

![Naming the server](/docs-img/first-server-create-name.png)

The portal hands the request to the underlying cloud and drops you back on the Servers list, where your new machine appears in a **Build** state with no address yet.

![A newly launched server in the Build state](/docs-img/first-server-building.png)

It moves through provisioning and settles on **Active** — usually under a minute for a small flavor — and picks up its IP addresses. (Curious what happens in between? See [How Provisioning Works](/docs/concepts/provisioning).)

## Step 8 — Connect and operate

Click the server's name to open its detail page. The header shows its size and IP addresses, a live status, power buttons (**Start / Stop / Reboot**) and a **More actions** menu; the tabs below cover Network, Security, Volumes, Events, Snapshots, Console log and Metadata.

![The server detail page, now Active with its IP addresses](/docs-img/first-server-detail.png)

Find the server's **public (floating) IP** here — in the header or the **Network** tab — and SSH in with the key from Step 2:

```
ssh -i my-first-key.pem <user>@<floating-ip>
```

The default `<user>` depends on the image — for Ubuntu it's `ubuntu`, or the username you set if you chose Password login. If the connection times out, confirm the server's security group allows port 22.

Everything else lives under **More actions**: **Rename, Resize, Rebuild, Rescue, Set password, Console (VNC),** and **Delete**.

![The More actions menu on a running server](/docs-img/first-server-actions.png)

## Step 9 — Clean up

When you're done, open **More actions → Delete** (or the ⋯ menu on the Servers list) and confirm. Deleting releases the compute so it stops accruing charges. If you assigned a floating IP and no longer need it, release it too from **Network → Floating IPs** — an idle floating IP can still cost a small amount.

That's the full loop: project → key → launch → connect → operate → clean up. To go deeper on any resource, see [Working with Servers](/docs/guides/servers) and [Networking](/docs/guides/networks) — or drive all of it from an assistant with [AI Agent Access](/docs/guides/ai-agents).
