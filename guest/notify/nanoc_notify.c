// SPDX-License-Identifier: GPL-2.0
/*
 * nanoc_notify: avisos de mudança feitos no Mac para arquivos compartilhados
 * por virtiofs. O guest não vê o que o Mac muda: o dentry e os atributos ficam
 * em cache e nenhum evento inotify é gerado. Cada linha escrita em
 * /dev/nanoc_notify é "<flags> <caminho absoluto>", com flags entre
 * c (criado), m (conteúdo mudou), a (atributos mudaram) e d (apagado).
 * O módulo descarta o dentry em cache, para que a próxima consulta vá ao host
 * e traga tamanho e atributos novos, e gera o evento inotify correspondente.
 */
#include <linux/module.h>
#include <linux/miscdevice.h>
#include <linux/fs.h>
#include <linux/namei.h>
#include <linux/dcache.h>
#include <linux/fsnotify.h>
#include <linux/slab.h>
#include <linux/uaccess.h>

#define NOTIFY_MAX_LINE 4096

/* o dentry do nome em cache, sem ir ao host (o FUSE não tem d_hash próprio) */
static struct dentry *cached_child(struct dentry *dir, struct qstr *name)
{
	name->hash = full_name_hash(dir, name->name, name->len);
	return d_lookup(dir, name);
}

/* descarta o dentry do nome em cache, positivo ou negativo */
static void drop_cached(struct dentry *dir, struct qstr *name)
{
	struct dentry *child = cached_child(dir, name);

	if (!IS_ERR_OR_NULL(child)) {
		d_invalidate(child);
		dput(child);
	}
}

static int notify_one(const char *flags, char *path)
{
	struct path parent, file;
	struct qstr name;
	char *slash = strrchr(path, '/');
	struct inode *dir;
	struct dentry *old;
	int err;

	if (path[0] != '/' || !slash || !slash[1])
		return -EINVAL;
	*slash = '\0';
	err = kern_path(slash == path ? "/" : path, LOOKUP_DIRECTORY, &parent);
	*slash = '/';
	if (err)
		return err;
	name = (struct qstr)QSTR_INIT(slash + 1, strlen(slash + 1));
	dir = d_inode(parent.dentry);

	if (strchr(flags, 'd')) {
		/* o dentry em cache ainda aponta para o arquivo apagado no host */
		old = cached_child(parent.dentry, &name);
		if (!IS_ERR_OR_NULL(old)) {
			if (d_really_is_positive(old))
				fsnotify_delete(dir, d_inode(old), old);
			d_invalidate(old);
			dput(old);
		}
		path_put(&parent);
		return 0;
	}

	drop_cached(parent.dentry, &name);
	err = kern_path(path, 0, &file);
	if (err) {
		path_put(&parent);
		return err;
	}
	if (strchr(flags, 'c'))
		fsnotify_create(dir, file.dentry);
	if (strchr(flags, 'm'))
		fsnotify_dentry(file.dentry, FS_MODIFY | FS_CLOSE_WRITE);
	if (strchr(flags, 'a'))
		fsnotify_dentry(file.dentry, FS_ATTRIB);
	path_put(&file);
	path_put(&parent);
	return 0;
}

static ssize_t notify_write(struct file *f, const char __user *buf, size_t len, loff_t *off)
{
	char *kbuf, *line, *next, *sp;
	int err;

	if (!capable(CAP_SYS_ADMIN))
		return -EPERM;
	if (len > NOTIFY_MAX_LINE * 16)
		return -E2BIG;
	kbuf = memdup_user_nul(buf, len);
	if (IS_ERR(kbuf))
		return PTR_ERR(kbuf);
	for (line = kbuf; line && *line; line = next) {
		next = strchr(line, '\n');
		if (next)
			*next++ = '\0';
		sp = strchr(line, ' ');
		if (!sp)
			continue;
		*sp = '\0';
		err = notify_one(line, sp + 1);
		if (err && err != -ENOENT)
			pr_info_ratelimited("nanoc_notify: %s: %d\n", sp + 1, err);
	}
	kfree(kbuf);
	return len;
}

static const struct file_operations notify_fops = {
	.owner = THIS_MODULE,
	.write = notify_write,
	.llseek = noop_llseek,
};

static struct miscdevice notify_dev = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "nanoc_notify",
	.fops = &notify_fops,
	.mode = 0600,
};

module_misc_device(notify_dev);
MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("nanoc: avisos de mudança de arquivos compartilhados feitas no host");
