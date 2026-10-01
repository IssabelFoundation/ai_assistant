#!/usr/bin/php
<?php
/* Idempotently register independent assistant permissions in Issabel ACL. */
$path = getenv('ISSABEL_ACL_DB');
if($path === false || $path === '') { $path = '/var/www/db/acl.db'; }
if(!is_readable($path) || !is_writable($path)) { fwrite(STDERR, "ACL database is not writable: $path\n"); exit(1); }

// Existing root-level menus may have no module access grant. Repair it on upgrade.
$permissions = array(
    'issabel_ai_assistant'=>'Asistente IA',
    'assistant.access'=>'Access the AI assistant',
    'assistant.provider.configure'=>'Configure a personal AI provider',
    'assistant.extensions.read'=>'Read extension information through the assistant',
    'assistant.plans.create'=>'Create and cancel extension change plans',
    'assistant.queues.read'=>'Read queue information through the assistant',
    'assistant.queues.plan'=>'Create and delete queue change plans',
    'assistant.namespace.read'=>'Check number availability and list PBX destinations through the assistant',
    'assistant.ringgroups.read'=>'Read ring group information through the assistant',
    'assistant.ringgroups.plan'=>'Create and delete ring group change plans',
    'assistant.time.read'=>'Read time groups and time conditions through the assistant',
    'assistant.time.plan'=>'Create and delete time group and time condition plans',
    'assistant.ivr.read'=>'Read IVR information through the assistant',
    'assistant.ivr.plan'=>'Create, update and delete IVR plans',
    'assistant.plans.approve'=>'Approve and execute extension change plans',
    'assistant.audit.read'=>'Read assistant plan audit records'
);

try {
    $db = new PDO('sqlite:'.$path);
    $db->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
    $db->beginTransaction();
    $transactionStarted = true;
    $adminGroup = $db->query("SELECT id FROM acl_group WHERE name='administrator' LIMIT 1")->fetchColumn();
    $accessAction = $db->query("SELECT id FROM acl_action WHERE name='access' LIMIT 1")->fetchColumn();
    if($adminGroup === false || $accessAction === false) { throw new RuntimeException('Required Issabel ACL records are missing'); }
    foreach($permissions as $name=>$description) {
        $select = $db->prepare('SELECT id FROM acl_resource WHERE name=? LIMIT 1');
        $select->execute(array($name));
        $resource = $select->fetchColumn();
        if($resource === false) {
            $resource = intval($db->query('SELECT COALESCE(MAX(id),0)+1 FROM acl_resource')->fetchColumn());
            $insert = $db->prepare('INSERT INTO acl_resource (id,name,description) VALUES (?,?,?)');
            $insert->execute(array($resource,$name,$description));
        }
        $grant = $db->prepare('SELECT id FROM acl_group_permission WHERE id_action=? AND id_group=? AND id_resource=? LIMIT 1');
        $grant->execute(array($accessAction,$adminGroup,$resource));
        if($grant->fetchColumn() === false) {
            $grantId = intval($db->query('SELECT COALESCE(MAX(id),0)+1 FROM acl_group_permission')->fetchColumn());
            $insertGrant = $db->prepare('INSERT INTO acl_group_permission (id,id_action,id_group,id_resource) VALUES (?,?,?,?)');
            $insertGrant->execute(array($grantId,$accessAction,$adminGroup,$resource));
        }
    }
    $db->commit();
    exit(0);
} catch(Exception $e) {
    if(isset($transactionStarted) && $transactionStarted) { $db->rollBack(); }
    fwrite(STDERR, "Unable to install assistant ACL: ".$e->getMessage()."\n");
    exit(1);
}
