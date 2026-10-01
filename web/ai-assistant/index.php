<?php
/* Issabel AI Assistant: same-origin web UI and authenticated local proxy. */
if(session_id() === '') {
    session_name('issabelSession');
    session_start();
}

function ai_b64url_decode($value) {
    $padding = strlen($value) % 4;
    if($padding) { $value .= str_repeat('=', 4-$padding); }
    return base64_decode(strtr($value, '-_', '+/'));
}

function ai_current_user() {
    if(isset($_SESSION['issabel_user']) && $_SESSION['issabel_user'] !== '') { return (string)$_SESSION['issabel_user']; }
    if(isset($_SESSION['elastix_user']) && $_SESSION['elastix_user'] !== '') { return (string)$_SESSION['elastix_user']; }
    if(isset($_SESSION['access_token'])) {
        $parts = explode('.', $_SESSION['access_token']);
        if(count($parts) === 3) {
            $payload = json_decode(ai_b64url_decode($parts[1]), true);
            if(isset($payload['sub']) && $payload['sub'] !== '') { return (string)$payload['sub']; }
            if(isset($payload['data']['name']) && $payload['data']['name'] !== '') { return (string)$payload['data']['name']; }
        }
    }
    return '';
}

function ai_has_permission($user, $permission) {
    if($user === 'admin') { return true; }
    try {
        static $db = null;
        if($db === null) {
            $db = new PDO('sqlite:/var/www/db/acl.db');
            $db->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
        }
        $sql = "SELECT 1 FROM acl_user u JOIN acl_user_permission p ON p.id_user=u.id JOIN acl_resource r ON r.id=p.id_resource JOIN acl_action a ON a.id=p.id_action WHERE u.name=? AND r.name=? AND a.name='access' UNION SELECT 1 FROM acl_user u JOIN acl_membership m ON m.id_user=u.id JOIN acl_group_permission p ON p.id_group=m.id_group JOIN acl_resource r ON r.id=p.id_resource JOIN acl_action a ON a.id=p.id_action WHERE u.name=? AND r.name=? AND a.name='access' LIMIT 1";
        $statement = $db->prepare($sql);
        $statement->execute(array($user,$permission,$user,$permission));
        return $statement->fetchColumn() !== false;
    } catch(Exception $e) { return false; }
}

function ai_permissions($user) {
    $all = array('assistant.access','assistant.provider.configure','assistant.extensions.read','assistant.plans.create','assistant.queues.read','assistant.queues.plan','assistant.ringgroups.read','assistant.ringgroups.plan','assistant.time.read','assistant.time.plan','assistant.ivr.read','assistant.ivr.plan','assistant.namespace.read','assistant.plans.approve','assistant.audit.read');
    if($user === 'admin') { return $all; }
    $result = array();
    foreach($all as $permission) { if(ai_has_permission($user, $permission)) { $result[] = $permission; } }
    return $result;
}

function ai_language() {
    $candidate = isset($_SESSION['issabel_language']) ? $_SESSION['issabel_language'] : (isset($_SESSION['language']) ? $_SESSION['language'] : '');
    if($candidate === '' && isset($_SERVER['HTTP_ACCEPT_LANGUAGE'])) { $candidate = substr($_SERVER['HTTP_ACCEPT_LANGUAGE'], 0, 2); }
    return strtolower(substr((string)$candidate, 0, 2)) === 'en' ? 'en' : 'es';
}

function ai_translations($language) {
    $file = dirname(__FILE__).'/lang/'.$language.'.lang';
    $translations = @parse_ini_file($file);
    return is_array($translations) ? $translations : array();
}

function ai_t($translations, $key, $fallback) { return isset($translations[$key]) ? $translations[$key] : $fallback; }

function ai_json($status, $data) {
    header('Content-Type: application/json; charset=UTF-8');
    header('Cache-Control: no-store');
    header((isset($_SERVER['SERVER_PROTOCOL']) ? $_SERVER['SERVER_PROTOCOL'] : 'HTTP/1.1').' '.$status, true, $status);
    echo json_encode($data);
    die();
}

function ai_set_status($status) {
    $protocol = isset($_SERVER['SERVER_PROTOCOL']) ? $_SERVER['SERVER_PROTOCOL'] : 'HTTP/1.1';
    header($protocol.' '.$status, true, $status);
}

function ai_random_token() {
    if(function_exists('random_bytes')) { return bin2hex(random_bytes(32)); }
    $bytes = openssl_random_pseudo_bytes(32, $strong);
    if($bytes === false || !$strong) { throw new RuntimeException('Secure random source unavailable'); }
    return bin2hex($bytes);
}

function ai_hash_equals($known, $provided) {
    if(function_exists('hash_equals')) { return hash_equals($known, $provided); }
    if(!is_string($known) || !is_string($provided) || strlen($known) !== strlen($provided)) { return false; }
    $difference = 0;
    for($i=0; $i<strlen($known); $i++) { $difference |= ord($known[$i]) ^ ord($provided[$i]); }
    return $difference === 0;
}

function ai_secret($file) {
    $raw = @file_get_contents($file);
    if($raw === false) { throw new RuntimeException('Local assistant secret is not readable by PHP'); }
    $value = trim($raw);
    $decoded = base64_decode($value, true);
    if($decoded !== false && strlen($decoded) === 32) { return $decoded; }
    if(strlen($value) === 32) { return $value; }
    throw new RuntimeException('Local assistant secret must contain 32 bytes or their base64 encoding');
}

function ai_origin_is_valid() {
    if(!isset($_SERVER['HTTP_ORIGIN']) || $_SERVER['HTTP_ORIGIN'] === '') { return true; }
    $origin = parse_url($_SERVER['HTTP_ORIGIN']);
    $host = isset($_SERVER['HTTP_HOST']) ? strtolower($_SERVER['HTTP_HOST']) : '';
    return isset($origin['host']) && strtolower($origin['host'].(isset($origin['port']) ? ':'.$origin['port'] : '')) === $host;
}

function ai_curl($url, $method, $body, $headers, $timeout = 90) {
    $curl = curl_init($url);
    curl_setopt($curl, CURLOPT_CUSTOMREQUEST, $method);
    curl_setopt($curl, CURLOPT_HTTPHEADER, $headers);
    curl_setopt($curl, CURLOPT_RETURNTRANSFER, true);
    curl_setopt($curl, CURLOPT_CONNECTTIMEOUT, 3);
    curl_setopt($curl, CURLOPT_TIMEOUT, $timeout);
    curl_setopt($curl, CURLOPT_HEADER, true);
    // Issabel commonly redirects its loopback PBX API to HTTPS with a locally
    // managed certificate. Never relax TLS verification for a remote host.
    $curlTarget = parse_url($url);
    $curlHost = isset($curlTarget['host']) ? strtolower($curlTarget['host']) : '';
    if(isset($curlTarget['scheme']) && strtolower($curlTarget['scheme']) === 'https' && in_array($curlHost, array('127.0.0.1','localhost','::1'), true)) {
        curl_setopt($curl, CURLOPT_SSL_VERIFYHOST, false);
        curl_setopt($curl, CURLOPT_SSL_VERIFYPEER, false);
    }
    if($body !== '') { curl_setopt($curl, CURLOPT_POSTFIELDS, $body); }
    $response = curl_exec($curl);
    $status = curl_getinfo($curl, CURLINFO_HTTP_CODE);
    $headerSize = curl_getinfo($curl, CURLINFO_HEADER_SIZE);
    $type = curl_getinfo($curl, CURLINFO_CONTENT_TYPE);
    $error = curl_error($curl);
    curl_close($curl);
    if($response === false) { return array(502, 'application/json', json_encode(array('status'=>'error','detail'=>$error))); }
    return array($status, $type, substr($response, $headerSize));
}

function ai_pbxapi_base() {
    $base = getenv('PBXAPI_INTERNAL_URL');
    if($base === false || $base === '') { return 'https://127.0.0.1/pbxapi'; }
    $base = rtrim($base, '/');
    if(preg_match('#^http://(127\.0\.0\.1|localhost)(:[0-9]+)?(/|$)#i', $base)) {
        $base = 'https://'.substr($base, 7);
    }
    return $base;
}

function ai_access_token_is_expiring() {
    if(empty($_SESSION['access_token'])) { return true; }
    $parts = explode('.', $_SESSION['access_token']);
    if(count($parts) !== 3) { return false; }
    $payload = json_decode(ai_b64url_decode($parts[1]), true);
    return is_array($payload) && isset($payload['exp']) && intval($payload['exp']) <= time()+30;
}

function ai_refresh_access_token($base) {
    if(empty($_SESSION['refresh_token'])) { return false; }
    $headers = array(
        'X-Refresh-Token: '.$_SESSION['refresh_token'],
        'Accept: application/json'
    );
    $response = ai_curl(rtrim($base,'/').'/authenticate/renewtoken', 'GET', '', $headers);
    if($response[0] < 200 || $response[0] >= 300) { return false; }
    $tokens = json_decode($response[2], true);
    if(!is_array($tokens) || !isset($tokens['access_token']) || $tokens['access_token'] === '') { return false; }
    $_SESSION['access_token'] = $tokens['access_token'];
    if(isset($tokens['refresh_token']) && $tokens['refresh_token'] !== '') {
        $_SESSION['refresh_token'] = $tokens['refresh_token'];
    }
    return true;
}

function ai_session_auth_headers($headers) {
    $result = array();
    foreach($headers as $header) {
        if(stripos($header, 'Authorization:') !== 0) { $result[] = $header; }
    }
    if(!empty($_SESSION['access_token'])) { $result[] = 'Authorization: Bearer '.$_SESSION['access_token']; }
    return $result;
}

function ai_pbxapi_request($base, $url, $method, $body, $headers) {
    if(ai_access_token_is_expiring()) { ai_refresh_access_token($base); }
    $response = ai_curl($url, $method, $body, ai_session_auth_headers($headers));
    if($response[0] === 401 && ai_refresh_access_token($base)) {
        $response = ai_curl($url, $method, $body, ai_session_auth_headers($headers));
    }
    if($response[0] === 401) {
        return array(401, 'application/json', json_encode(array(
            'status'=>'unauthorized',
            'detail'=>'La sesión de PBX API venció y no pudo renovarse. Cierra la sesión de Issabel e ingresa nuevamente.'
        )));
    }
    return $response;
}

function ai_plan_outcome($response) {
    if($response[0] >= 200 && $response[0] < 300) { return 'executed'; }
    $data = json_decode($response[2], true);
    if(is_array($data) && isset($data['status']) && $data['status'] === 'failed') { return 'execution_failed'; }
    return ($response[0] >= 500 || $response[0] === 409) ? 'unknown' : 'execution_failed';
}

// Report only a server-observed outcome, never credentials or arbitrary browser text.
function ai_record_plan_result($user, $id, $outcome) {
    $messages = array(
        'executed'=>'El plan '.$id.' ha sido ejecutado con éxito.',
        'approval_failed'=>'No se pudo aprobar el plan '.$id.'. No se solicitó su ejecución.',
        'execution_failed'=>'La ejecución del plan '.$id.' devolvió un error. Revisá su auditoría antes de volver a intentarlo.',
        'unknown'=>'No se pudo confirmar el resultado del plan '.$id.'. Revisá su estado y auditoría antes de volver a intentarlo.'
    );
    header('X-Issabel-Plan-Message: '.rawurlencode($messages[$outcome]));
    header('X-Issabel-Plan-Outcome: '.$outcome);
    $saved = false;
    try {
        $path = '/v1/plan-result';
        $body = json_encode(array('plan_id'=>$id, 'outcome'=>$outcome));
        $stamp = (string)time();
        $permissions = ai_permissions($user); sort($permissions, SORT_STRING);
        $permissionHeader = implode(',', $permissions);
        $canonical = "POST\n".$path."\n".$user."\n".$permissionHeader."\n".$stamp."\n".hash('sha256', $body);
        $signature = hash_hmac('sha256', $canonical, ai_secret('/etc/issabel-mcp/web.secret'));
        $response = ai_curl('http://127.0.0.1:8787'.$path, 'POST', $body, array(
            'X-Issabel-User: '.$user, 'X-Issabel-Permissions: '.$permissionHeader,
            'X-Issabel-Timestamp: '.$stamp, 'X-Issabel-Signature: '.$signature,
            'Content-Type: application/json'
        ), 5);
        $saved = $response[0] >= 200 && $response[0] < 300;
    } catch(Exception $e) { /* Keep the execution response, even if history is unavailable. */ }
    header('X-Issabel-Plan-History: '.($saved ? 'saved' : 'failed'));
    if(!$saved) { error_log('issabel-ai: plan result history could not be saved for '.$id); }
}

$aiUser = ai_current_user();
if($aiUser === '' || !ai_has_permission($aiUser, 'assistant.access')) {
    ai_json(403, array('status'=>'forbidden','detail'=>'A valid Issabel session and Assistant access are required'));
}

if(!isset($_SESSION['issabel_ai_csrf'])) { $_SESSION['issabel_ai_csrf'] = ai_random_token(); }
$aiLanguage = ai_language();
$aiTranslations = ai_translations($aiLanguage);

if(isset($_GET['api'])) {
    if(!ai_origin_is_valid()) { ai_json(403, array('status'=>'forbidden','detail'=>'Invalid origin')); }
    $method = isset($_SERVER['REQUEST_METHOD']) ? $_SERVER['REQUEST_METHOD'] : 'GET';
    if($method !== 'GET') {
        $provided = isset($_SERVER['HTTP_X_CSRF_TOKEN']) ? $_SERVER['HTTP_X_CSRF_TOKEN'] : '';
        $validCsrf = ai_hash_equals($_SESSION['issabel_ai_csrf'], $provided);
        if(!$validCsrf) { ai_json(403, array('status'=>'forbidden','detail'=>'Invalid CSRF token')); }
    }

    $api = $_GET['api'];
    if($api === 'approve_execute') {
        if($method !== 'POST') { ai_json(405, array('status'=>'error','detail'=>'Method not allowed')); }
        if(!ai_has_permission($aiUser, 'assistant.plans.approve')) { ai_json(403, array('status'=>'forbidden')); }
        $id = isset($_GET['id']) ? $_GET['id'] : '';
        if(!preg_match('/^[a-f0-9-]{36}$/', $id)) { ai_json(400, array('status'=>'error','detail'=>'Invalid plan')); }
        $base = ai_pbxapi_base();
        $auth = array('Content-Type: application/json', 'Accept: application/json');
        $executionBody = file_get_contents('php://input');
        if($executionBody === '' || strlen($executionBody) > 65536 || !is_array(json_decode($executionBody, true))) { ai_json(400, array('status'=>'error','detail'=>'Invalid execution input')); }
        $approved = ai_pbxapi_request($base, rtrim($base,'/').'/mcpplans/'.$id.'?action=approve', 'PUT', '{}', $auth);
        if(($approved[0] < 200 || $approved[0] >= 300) && $approved[0] !== 409) { ai_record_plan_result($aiUser, $id, 'approval_failed'); header('Content-Type: '.$approved[1]); ai_set_status($approved[0]); echo $approved[2]; die(); }
        $executed = ai_pbxapi_request($base, rtrim($base,'/').'/mcpplans/'.$id.'?action=execute', 'PUT', $executionBody, $auth);
        $outcome = ai_plan_outcome($executed);
        ai_record_plan_result($aiUser, $id, $outcome);
        header('Content-Type: '.$executed[1]);
        header('Cache-Control: no-store');
        if(strpos($executed[1], 'text/csv') !== false) { header('Content-Disposition: attachment; filename="issabel-extension-credentials-'.$id.'.csv"'); }
        ai_set_status($executed[0]); echo $executed[2]; die();
    }
    if($api === 'plans') {
        if(!ai_has_permission($aiUser, 'assistant.plans.approve')) { ai_json(403, array('status'=>'forbidden')); }
        $base = ai_pbxapi_base();
        $listed = ai_pbxapi_request($base, rtrim($base,'/').'/mcpplans', 'GET', '', array('Accept: application/json'));
        header('Content-Type: '.$listed[1]); ai_set_status($listed[0]); echo $listed[2]; die();
    }
    if($api === 'cancel_plan') {
        if(!ai_has_permission($aiUser, 'assistant.plans.create') && !ai_has_permission($aiUser, 'assistant.plans.approve')) { ai_json(403, array('status'=>'forbidden')); }
        $id = isset($_GET['id']) ? $_GET['id'] : '';
        if(!preg_match('/^[a-f0-9-]{36}$/', $id)) { ai_json(400, array('status'=>'error','detail'=>'Invalid plan')); }
        $base = ai_pbxapi_base();
        $cancelled = ai_pbxapi_request($base, rtrim($base,'/').'/mcpplans/'.$id, 'DELETE', '', array('Accept: application/json'));
        header('Content-Type: '.$cancelled[1]); ai_set_status($cancelled[0]); echo $cancelled[2]; die();
    }
    if($api === 'plan_audit') {
        if(!ai_has_permission($aiUser, 'assistant.audit.read')) { ai_json(403, array('status'=>'forbidden')); }
        $id = isset($_GET['id']) ? $_GET['id'] : '';
        if(!preg_match('/^[a-f0-9-]{36}$/', $id)) { ai_json(400, array('status'=>'error','detail'=>'Invalid plan')); }
        $base = ai_pbxapi_base();
        $audit = ai_pbxapi_request($base, rtrim($base,'/').'/mcpplans/'.$id.'?audit=1', 'GET', '', array('Accept: application/json'));
        header('Content-Type: '.$audit[1]); ai_set_status($audit[0]); echo $audit[2]; die();
    }

    $routes = array(
        'provider'=>array('/v1/provider','assistant.provider.configure'),
        'provider_test'=>array('/v1/provider/test','assistant.provider.configure'),
        'models'=>array('/v1/models','assistant.provider.configure'),
        'conversations'=>array('/v1/conversations','assistant.access'),
        'conversation'=>array('/v1/conversations/'.(isset($_GET['id']) ? rawurlencode($_GET['id']) : ''),'assistant.access'),
        'chat'=>array('/v1/chat','assistant.plans.create'),
        'chat_stream'=>array('/v1/chat/stream','assistant.plans.create')
    );
    if(!isset($routes[$api])) { ai_json(404, array('status'=>'not_found')); }
    if(!ai_has_permission($aiUser, $routes[$api][1])) { ai_json(403, array('status'=>'forbidden')); }
    $body = file_get_contents('php://input');
    $path = $routes[$api][0];
    $stamp = (string)time();
    $permissions = ai_permissions($aiUser); sort($permissions, SORT_STRING); $permissionHeader = implode(',', $permissions);
    $canonical = $method."\n".$path."\n".$aiUser."\n".$permissionHeader."\n".$stamp."\n".hash('sha256', $body);
    try { $signature = hash_hmac('sha256', $canonical, ai_secret('/etc/issabel-mcp/web.secret')); }
    catch(Exception $e) {
        error_log('issabel-ai: '.$e->getMessage());
        ai_json(503, array('status'=>'error','detail'=>'Assistant service is not configured'));
    }
    $proxyHeaders = array(
        'X-Issabel-User: '.$aiUser,
        'X-Issabel-Permissions: '.$permissionHeader,
        'X-Issabel-Timestamp: '.$stamp,
        'X-Issabel-Signature: '.$signature,
        'Content-Type: application/json',
        'Accept: '.($api === 'chat_stream' ? 'text/event-stream' : 'application/json')
    );
    if($api === 'chat_stream') {
        header('Content-Type: text/event-stream; charset=UTF-8');
        header('Cache-Control: no-store');
        header('X-Accel-Buffering: no');
        $curl = curl_init('http://127.0.0.1:8787'.$path);
        curl_setopt($curl, CURLOPT_CUSTOMREQUEST, $method);
        curl_setopt($curl, CURLOPT_HTTPHEADER, $proxyHeaders);
        curl_setopt($curl, CURLOPT_POSTFIELDS, $body);
        curl_setopt($curl, CURLOPT_CONNECTTIMEOUT, 3);
        curl_setopt($curl, CURLOPT_TIMEOUT, 90);
        curl_setopt($curl, CURLOPT_WRITEFUNCTION, function($curl, $chunk) { echo $chunk; if(function_exists('ob_flush')) { @ob_flush(); } flush(); return strlen($chunk); });
        if(curl_exec($curl) === false) { echo "event: error\ndata: ".json_encode(array('error'=>'Assistant service unavailable'))."\n\n"; }
        curl_close($curl);
        die();
    }
    $response = ai_curl('http://127.0.0.1:8787'.$path, $method, $body, $proxyHeaders);
    header('Content-Type: '.($response[1] ? $response[1] : 'application/json'));
    header('Cache-Control: no-store');
    ai_set_status($response[0]); echo $response[2]; die();
}

function _moduleContent(&$smarty, $module_name) {
    global $aiLanguage, $aiTranslations;
    $title = htmlspecialchars(ai_t($aiTranslations,'assistant_title','Asistente IA'),ENT_QUOTES,'UTF-8');
    $pending = htmlspecialchars(ai_t($aiTranslations,'pending_plans','Planes pendientes'),ENT_QUOTES,'UTF-8');
    $settings = htmlspecialchars(ai_t($aiTranslations,'provider_settings','Proveedor y modelo'),ENT_QUOTES,'UTF-8');
    $notice = htmlspecialchars(ai_t($aiTranslations,'privacy_notice','Tus mensajes y los datos de PBX necesarios para responder se enviarán al proveedor que configures. Las claves quedan cifradas localmente.'),ENT_QUOTES,'UTF-8');
    $history = htmlspecialchars(ai_t($aiTranslations,'history','Historial local (30 días)'),ENT_QUOTES,'UTF-8');
    $csrf = htmlspecialchars($_SESSION['issabel_ai_csrf'], ENT_QUOTES, 'UTF-8');
    ob_start();
?>
<main class="issabel-ai" data-csrf="<?php echo $csrf; ?>" lang="<?php echo $aiLanguage; ?>">
    <header class="ai-header"><div><span class="ai-eyebrow">ISSABEL</span><h1><?php echo $title; ?></h1></div><div class="ai-actions"><button type="button" id="pendingButton" class="ai-secondary"><?php echo $pending; ?></button><button type="button" id="settingsButton" class="ai-secondary"><?php echo $settings; ?></button></div></header>
    <p class="ai-notice"><?php echo $notice; ?></p>
    <section id="settings" class="ai-panel ai-hidden">
        <h2>Configuración BYOK</h2>
        <div class="ai-grid"><label>Proveedor<select id="provider"><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option><option value="gemini">Gemini</option></select></label><label>Modelo<input id="model" list="modelOptions" autocomplete="off" placeholder="Seleccione o escriba un modelo"><datalist id="modelOptions"></datalist></label><label>API key<input id="apiKey" type="password" autocomplete="new-password" placeholder="Sólo se usa al guardar"></label></div>
        <div class="ai-actions"><button type="button" id="saveProvider">Guardar</button><button type="button" id="testProvider" class="ai-secondary">Probar conexión</button><button type="button" id="deleteProvider" class="ai-danger">Borrar clave</button></div><p id="providerStatus"></p>
    </section>
    <section id="messages" class="ai-messages" aria-live="polite"><article class="assistant">Hola. Puedo consultar extensiones, colas, grupos de timbrado, horarios y destinos, verificar si un número está libre, y preparar planes con aprobación humana para extensiones SIP, PJSIP y PJSIP WebRTC, colas, grupos de timbrado y condiciones horarias. Cada cambio requerirá tu aprobación aquí.</article></section>
    <section id="plans"></section>
    <form id="chatForm" class="ai-chat-form"><textarea id="message" maxlength="12000" required placeholder="Ej.: crea 10 extensiones PJSIP desde la 100, sin voicemail"></textarea><button type="submit">Enviar</button></form>
    <footer class="ai-footer"><button type="button" id="historyButton" class="ai-link"><?php echo $history; ?></button><span id="activity"></span></footer>
    <section id="history" class="ai-panel ai-hidden"><h2>Conversaciones</h2><div id="historyList"></div></section>
</main>
<?php
    return ob_get_clean();
}
