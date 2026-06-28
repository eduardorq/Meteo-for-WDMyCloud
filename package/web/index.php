<?php
$host = isset($_SERVER['HTTP_HOST']) ? $_SERVER['HTTP_HOST'] : $_SERVER['SERVER_ADDR'];
$host = preg_replace('/:\d+$/', '', $host);
header('Location: http://' . $host . ':8098/', true, 302);
exit;
?>
