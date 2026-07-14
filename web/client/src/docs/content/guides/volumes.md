# Block Volumes and Snapshots

Block storage gives your servers persistent disks that outlive any single boot. The **Storage** group in the sidebar covers block volumes and their snapshots, alongside file shares (mountable shared file systems) and object storage buckets — the latter has [its own guide](/docs/guides/object-storage).

## Volumes

A **volume** (under **Storage → Volumes**) is a block device you attach to a server, where it appears as an extra disk. Volumes are independent of the server's lifecycle: detach one from a machine you're deleting and reattach it elsewhere, and its data comes along.

Create one with **Create volume** — give it a name and a size in GB (leave the volume type blank to use the project default). It starts out empty.

![Creating a block volume](/docs-img/create-volume.png)

Once it exists, a volume's actions menu lets you **attach it to a server**, **extend** it (grow the disk), **change its type**, or delete it. After attaching, format and mount it from inside the guest OS as you would any disk.

![A volume attached to a server, on the server's Volumes tab](/docs-img/volume-attach.png)

## Snapshots

A **snapshot** (under **Storage → Snapshots**) is a point-in-time copy of a volume. Take one before a risky change so you can roll back, or use it as the basis for a new volume. Snapshots capture the volume as it stands at the moment you create them, so quiesce or unmount the volume first if you need a fully consistent image.

![The Snapshots list with a volume snapshot](/docs-img/volume-snapshot.png)

## File shares

For storage that several servers mount at once, **Storage → File shares** provides shared file systems rather than single-attach block disks — useful when multiple machines need to read and write the same files concurrently.
